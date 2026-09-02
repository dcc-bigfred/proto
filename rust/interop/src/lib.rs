//! Network interop: Rust no_std codecs + `std::net` against Go `Listen`.
#![allow(missing_docs)]

#[cfg(test)]
mod tests {
    use dcc_proto_z21 as z21;
    use std::io::{BufRead, BufReader, Write};
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
            c.args(["run", "./cmd/loopback-host", "--", "--expect", expect])
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

    #[test]
    fn rust_z21_client_go_server() {
        let mut host = spawn_host("2");
        let sock = UdpSocket::bind("127.0.0.1:0").unwrap();
        sock.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        let mut pkt = z21::WireBuf::new();
        z21::encode_get_serial(&mut pkt).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        let mut buf = [0u8; 256];
        let _ = sock.recv_from(&mut buf).unwrap();
        pkt.clear();
        z21::encode_set_drive(&mut pkt, 3, 50, true, 3).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        pkt.clear();
        z21::encode_set_function(&mut pkt, 3, 0, true).unwrap();
        sock.send_to(&pkt, &host.z21).unwrap();
        let got = events(&mut host, 2);
        assert_eq!(
            got,
            vec![
                "SetSpeed 3 50 true 128".to_string(),
                "SetFunction 3 0 true".to_string()
            ]
        );
    }

    #[test]
    fn rust_withrottle_client_go_server() {
        let mut host = spawn_host("3");
        let mut stream = TcpStream::connect(&host.wt).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        stream.write_all(b"HUinterop\n").unwrap();
        let mut r = BufReader::new(stream.try_clone().unwrap());
        let mut line = String::new();
        r.read_line(&mut line).unwrap();
        assert!(line.starts_with("VN"), "handshake {line:?}");
        stream.write_all(b"M0+S3<;>S3\n").unwrap();
        stream.write_all(b"M0AS3<;>R1\n").unwrap();
        stream.write_all(b"M0AS3<;>V50\n").unwrap();
        stream.write_all(b"M0AS3<;>F10\n").unwrap();
        stream.write_all(b"M0AS3<;>F00\n").unwrap();
        let got = events(&mut host, 3);
        assert_eq!(
            got,
            vec![
                "SetSpeed 3 0 true 128".to_string(),
                "SetSpeed 3 50 true 128".to_string(),
                "SetFunction 3 0 true".to_string()
            ]
        );
    }
}
