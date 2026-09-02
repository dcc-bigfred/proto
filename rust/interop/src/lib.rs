//! Network interop: Rust no_std codecs + `std::net` against Go `Listen`.
#![allow(missing_docs)]

#[cfg(test)]
mod tests {
    use dcc_proto_z21 as z21;
    use std::io::{BufRead, BufReader, Write};
    use std::net::{TcpStream, UdpSocket};
    use std::path::PathBuf;
    use std::process::{Command, Stdio};
    use std::time::Duration;

    fn go_dir() -> PathBuf {
        if let Ok(p) = std::env::var("PROTO_GO_DIR") {
            return PathBuf::from(p);
        }
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../go")
    }

    fn spawn_host() -> (std::process::Child, String, String) {
        let mut child = Command::new("go")
            .args(["run", "./cmd/loopback-host"])
            .current_dir(go_dir())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
            .expect("go run ./cmd/loopback-host (install Go to run interop)");
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
        (child, z21, wt)
    }

    #[test]
    fn rust_z21_client_go_server() {
        let (mut child, addr, _wt) = spawn_host();
        let sock = UdpSocket::bind("127.0.0.1:0").unwrap();
        sock.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        let mut pkt = z21::WireBuf::new();
        z21::encode_get_serial(&mut pkt).unwrap();
        sock.send_to(&pkt, &addr).unwrap();
        let mut buf = [0u8; 256];
        let _ = sock.recv_from(&mut buf).unwrap();
        pkt.clear();
        z21::encode_set_drive(&mut pkt, 3, 50, true, 3).unwrap();
        sock.send_to(&pkt, &addr).unwrap();
        let status = child.wait().expect("wait host");
        assert!(status.success(), "loopback-host exit {status}");
    }

    #[test]
    fn rust_withrottle_client_go_server() {
        let (mut child, _z21, addr) = spawn_host();
        let mut stream = TcpStream::connect(&addr).unwrap();
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
        let status = child.wait().expect("wait host");
        assert!(status.success(), "loopback-host exit {status}");
    }
}
