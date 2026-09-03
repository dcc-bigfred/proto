//! Experimental WiThrottle TCP server. Public API mirrors Go `withrottle.Listen`.
#![allow(missing_docs)]

use dcc_bigfred_proto_withrottle as wt;
use std::io::{BufRead, BufReader, Write};
use std::net::{TcpListener, TcpStream, SocketAddr};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;

/// Consumer of inbound drive commands.
pub trait DriveHost: Send + Sync {
    fn set_speed(&self, addr: u16, speed: u8, forward: bool, steps: u8);
    fn set_function(&self, addr: u16, func: u8, on: bool);
}

/// TCP listener.
pub struct Server {
    ln: TcpListener,
    stop: Arc<AtomicBool>,
}

impl Server {
    pub fn listen(bind: &str, host: Arc<dyn DriveHost>) -> std::io::Result<Self> {
        let ln = TcpListener::bind(bind)?;
        ln.set_nonblocking(true)?;
        let accept = ln.try_clone()?;
        let stop = Arc::new(AtomicBool::new(false));
        let flag = stop.clone();
        thread::spawn(move || accept_loop(accept, host, flag));
        Ok(Self { ln, stop })
    }

    pub fn local_addr(&self) -> std::io::Result<SocketAddr> {
        self.ln.local_addr()
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

fn accept_loop(ln: TcpListener, host: Arc<dyn DriveHost>, stop: Arc<AtomicBool>) {
    while !stop.load(Ordering::Relaxed) {
        match ln.accept() {
            Ok((stream, _)) => {
                let h = host.clone();
                thread::spawn(move || serve(stream, h));
            }
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                thread::sleep(std::time::Duration::from_millis(20));
            }
            Err(_) => return,
        }
    }
}

fn serve(mut stream: TcpStream, host: Arc<dyn DriveHost>) {
    let mut reader = BufReader::new(stream.try_clone().unwrap());
    let mut line = String::new();
    loop {
        line.clear();
        match reader.read_line(&mut line) {
            Ok(0) | Err(_) => return,
            Ok(_) => {}
        }
        let s = line.trim_end_matches(['\r', '\n']);
        if s.starts_with("HU") {
            let _ = write!(stream, "VN2.0\n*10\nPPA1\nRL0\nHTproto\n");
            continue;
        }
        if !wt::is_multithrottle_line(s.as_bytes()) || s.len() < 4 {
            continue;
        }
        let op = s.as_bytes()[2];
        if op == b'+' {
            let _ = writeln!(stream, "M0+S3<;>");
            continue;
        }
        if op != b'A' {
            continue;
        }
        let rest = &s[3..];
        let Some(sep) = rest.find("<;>") else {
            continue;
        };
        let key = &rest[..sep];
        let prop = &rest[sep + 3..];
        let addr = parse_key(key);
        if let Some(addr) = addr {
            if let Some(speed) = prop.strip_prefix('V').and_then(|p| p.parse().ok()) {
                host.set_speed(addr, speed, true, 128);
            }
            if let Some(rest) = prop.strip_prefix('R') {
                let forward = rest.as_bytes().first().copied() != Some(b'0');
                host.set_speed(addr, 0, forward, 128);
            }
            if prop.starts_with('f') || prop.starts_with('F') {
                if prop.len() >= 2 {
                    let on = prop.as_bytes()[1] == b'1';
                    if let Ok(func) = prop[2..].parse::<u8>() {
                        host.set_function(addr, func, on);
                    }
                }
            }
        }
    }
}

fn parse_key(s: &str) -> Option<u16> {
    let b = s.as_bytes().first()?;
    match b {
        b'S' | b's' | b'L' | b'l' => s[1..].parse().ok(),
        _ => None,
    }
}

/// In-memory host for tests.
#[derive(Default)]
pub struct Recorder {
    inner: Mutex<Option<(u16, u8, bool)>>,
}

impl Recorder {
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }
    pub fn last_speed(&self) -> Option<(u16, u8, bool)> {
        *self.inner.lock().unwrap()
    }
}

impl DriveHost for Recorder {
    fn set_speed(&self, addr: u16, speed: u8, forward: bool, _steps: u8) {
        *self.inner.lock().unwrap() = Some((addr, speed, forward));
    }
    fn set_function(&self, _addr: u16, _func: u8, _on: bool) {}
}
