//! Network interop: Rust no_std codecs + `std::net` against Go `Listen`.
#![allow(missing_docs)]

#[cfg(test)]
mod tests {
    use dcc_proto_withrottle as wt;
    use dcc_proto_z21 as z21;
    use std::io::{BufRead, BufReader, Read, Write};
    use std::net::{TcpStream, UdpSocket};
    use std::path::PathBuf;
    use std::process::{Child, Command, Stdio};
    use std::time::Duration;

    fn go_dir() -> PathBuf {
        if let Ok(p) = std::env::var("PROTO_GO_DIR") {
            return PathBuf::from(p);
        }
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../go")
    }

    struct Host {
        child: Child,
        lines: std::io::Lines<BufReader<std::process::ChildStdout>>,
        z21: String,
        wt: String,
    }

    fn spawn_host(expect: &str) -> Host {
        let mut cmd = if let Ok(bin) = std::env::var("PROTO_LOOPBACK_HOST") {
            let mut c = Command::new(bin);
            c.arg("--expect").arg(expect);
            c
        } else {
            let mut c = Command::new("go");
            c.args(["run", "./cmd/loopback-host", "--expect", expect])
                .current_dir(go_dir());
            c
        };
        let mut child = cmd
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .expect("loopback-host (set PROTO_LOOPBACK_HOST or install Go)");
        let stdout = child.stdout.take().expect("stdout");
        let mut lines = BufReader::new(stdout).lines();
        let z21 = lines
            .next()
            .expect("Z21 line")
            .expect("read")
            .trim_start_matches("Z21=")
            .to_string();
        let wt = lines
            .next()
            .expect("WT line")
            .expect("read")
            .trim_start_matches("WT=")
            .to_string();
        Host {
            child,
            lines,
            z21,
            wt,
        }
    }

    fn events(host: &mut Host, n: usize) -> Vec<String> {
        let mut out = Vec::with_capacity(n);
        for _ in 0..n {
            let line = host.lines.next().expect("event").expect("read");
            out.push(line);
        }
        let status = host.child.wait().expect("wait host");
        assert!(status.success(), "loopback-host exit {status}");
        out
    }

    fn recv_udp(sock: &UdpSocket) -> Vec<u8> {
        let mut buf = [0u8; 256];
        let (n, _) = sock.recv_from(&mut buf).unwrap();
        buf[..n].to_vec()
    }

    fn write_wt(stream: &mut TcpStream, cli: &wt::Client, cmd: wt::Command) {
        let mut out = wt::WireBuf::new();
        cli.encode(&cmd, &mut out).unwrap();
        stream.write_all(out.as_slice()).unwrap();
    }

    #[test]
    fn rust_z21_client_go_server() {
        let mut host = spawn_host("4");
        let sock = UdpSocket::bind("127.0.0.1:0").unwrap();
        sock.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        let mut pkt = z21::WireBuf::new();
        z21::encode_get_serial(&mut pkt).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        let _serial = recv_udp(&sock);

        pkt.clear();
        z21::encode_set_drive(&mut pkt, 3, 50, true, 3).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        let info = z21::parse_loco_info(&recv_udp(&sock)).expect("loco info after SET_DRIVE");
        assert_eq!(info.addr, 3);
        assert_eq!(info.speed, 50);
        assert!(info.forward, "SET nibble 3 vs INFO KKK=4");

        pkt.clear();
        z21::encode_set_function(&mut pkt, 3, 0, true).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        let _ = recv_udp(&sock);

        pkt.clear();
        z21::encode_track_power(&mut pkt, true).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();

        pkt.clear();
        z21::encode_set_drive(&mut pkt, 128, 30, true, 3).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        let long = z21::parse_loco_info(&recv_udp(&sock)).expect("loco info long addr");
        assert_eq!(long.addr, 128);
        assert_eq!(long.speed, 30);

        let got = events(&mut host, 4);
        assert_eq!(
            got,
            vec![
                "SetSpeed 3 50 true 128".to_string(),
                "SetFunction 3 0 true".to_string(),
                "SetTrackPower true".to_string(),
                "SetSpeed 128 30 true 128".to_string(),
            ]
        );
    }

    #[test]
    fn rust_withrottle_client_go_server() {
        let mut host = spawn_host("8");
        let mut stream = TcpStream::connect(&host.wt).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(3)))
            .unwrap();

        let mut cli = wt::Client::new("proto", "interop");
        let mut hello = wt::WireBuf::new();
        cli.on_connect(&mut hello).unwrap();
        stream.write_all(hello.as_slice()).unwrap();

        let mut inbound = Vec::new();
        let mut buf = [0u8; 512];
        for _ in 0..16 {
            let n = stream.read(&mut buf).expect("handshake burst");
            assert!(n > 0, "EOF during handshake");
            cli.on_bytes(&buf[..n], &mut |e| inbound.push(e));
            let has_proto = inbound.iter().any(|e| matches!(e, wt::Event::Protocol));
            let has_hb = inbound.iter().any(|e| matches!(e, wt::Event::Heartbeat));
            let has_pwr = inbound
                .iter()
                .any(|e| matches!(e, wt::Event::TrackPower { on: true }));
            if has_proto && has_hb && has_pwr {
                break;
            }
        }
        assert!(
            inbound.iter().any(|e| matches!(e, wt::Event::Protocol)),
            "VN in burst: {inbound:?}"
        );
        assert!(
            inbound.iter().any(|e| matches!(e, wt::Event::Heartbeat)),
            "* in burst: {inbound:?}"
        );
        assert!(
            inbound
                .iter()
                .any(|e| matches!(e, wt::Event::TrackPower { on: true })),
            "PPA1 in burst: {inbound:?}"
        );

        write_wt(&mut stream, &cli, wt::Command::Acquire { addr: 3 });
        write_wt(
            &mut stream,
            &cli,
            wt::Command::SetDirection {
                addr: 3,
                forward: true,
            },
        );
        write_wt(
            &mut stream,
            &cli,
            wt::Command::SetSpeed { addr: 3, speed: 50 },
        );
        write_wt(
            &mut stream,
            &cli,
            wt::Command::SetFunction {
                addr: 3,
                func: 0,
                on: true,
            },
        );
        // P6.0 press/release on a different function (latching F1).
        stream.write_all(b"M0AS3<;>F11\n").unwrap();
        stream.write_all(b"M0AS3<;>F01\n").unwrap();
        write_wt(&mut stream, &cli, wt::Command::TrackPower { on: false });
        write_wt(&mut stream, &cli, wt::Command::Acquire { addr: 128 });
        write_wt(
            &mut stream,
            &cli,
            wt::Command::SetSpeed {
                addr: 128,
                speed: 40,
            },
        );
        stream.write_all(b"M0-S3<;>\n").unwrap();
        stream.write_all(b"M0-L128<;>\n").unwrap();

        let got = events(&mut host, 8);
        assert_eq!(
            got,
            vec![
                "SetSpeed 3 0 true 128".to_string(),
                "SetSpeed 3 50 true 128".to_string(),
                "SetFunction 3 0 true".to_string(),
                "SetFunction 3 1 true".to_string(),
                "SetTrackPower false".to_string(),
                "SetSpeed 128 40 true 128".to_string(),
                "Release 3".to_string(),
                "Release 128".to_string(),
            ]
        );
    }
}
