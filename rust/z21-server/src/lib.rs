//! Experimental Z21 LAN UDP server. Public API mirrors Go `z21.Listen`.
#![allow(missing_docs)]

use dcc_bigfred_proto_z21 as z21;
use std::net::{SocketAddr, UdpSocket};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;

/// Consumer of inbound drive commands.
pub trait DriveHost: Send + Sync {
    fn set_speed(&self, addr: u16, speed: u8, forward: bool, steps: u8);
    fn set_function(&self, addr: u16, func: u8, on: bool);
    fn loco_state(&self, addr: u16) -> (u8, bool, u32);
}

/// UDP listener.
pub struct Server {
    sock: UdpSocket,
    stop: Arc<AtomicBool>,
}

impl Server {
    /// Bind and serve in a background thread. `bind` may be `127.0.0.1:0`.
    pub fn listen(bind: &str, host: Arc<dyn DriveHost>) -> std::io::Result<Self> {
        let sock = UdpSocket::bind(bind)?;
        sock.set_read_timeout(Some(std::time::Duration::from_millis(200)))?;
        let reader = sock.try_clone()?;
        let stop = Arc::new(AtomicBool::new(false));
        let flag = stop.clone();
        thread::spawn(move || serve(reader, host, flag));
        Ok(Self { sock, stop })
    }

    #[must_use]
    pub fn local_addr(&self) -> std::io::Result<SocketAddr> {
        self.sock.local_addr()
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
    }
}

fn serve(sock: UdpSocket, host: Arc<dyn DriveHost>, stop: Arc<AtomicBool>) {
    let mut buf = [0u8; 2048];
    let serial: u32 = 258_000_001;
    while !stop.load(Ordering::Relaxed) {
        let (n, from) = match sock.recv_from(&mut buf) {
            Ok(v) => v,
            Err(_) => continue,
        };
        let mut pkts: heapless::Vec<&[u8], 8> = heapless::Vec::new();
        z21::split_datagram(&buf[..n], &mut pkts);
        for pkt in pkts {
            handle(&sock, from, pkt, &*host, serial);
        }
    }
}

fn handle(sock: &UdpSocket, from: SocketAddr, pkt: &[u8], host: &dyn DriveHost, serial: u32) {
    if pkt.len() >= 4 {
        let header = u16::from_le_bytes([pkt[2], pkt[3]]);
        if header == z21::HEADER_GET_SERIAL {
            let mut out = z21::WireBuf::new();
            let _ = out.extend_from_slice(&8u16.to_le_bytes());
            let _ = out.extend_from_slice(&z21::HEADER_GET_SERIAL.to_le_bytes());
            let _ = out.extend_from_slice(&serial.to_le_bytes());
            let _ = sock.send_to(&out, from);
            return;
        }
    }
    if let Some(info) = parse_set_drive(pkt) {
        host.set_speed(info.0, info.1, info.2, 128);
        echo_loco(sock, from, host, info.0);
        return;
    }
    if let Some((addr, func, on)) = parse_set_function(pkt) {
        host.set_function(addr, func, on);
        echo_loco(sock, from, host, addr);
    }
}

fn parse_set_drive(pkt: &[u8]) -> Option<(u16, u8, bool)> {
    if pkt.len() < 10 {
        return None;
    }
    let header = u16::from_le_bytes([pkt[2], pkt[3]]);
    if header != z21::HEADER_XBUS || pkt[4] != 0xE4 || pkt[5] & 0xF0 != 0x10 {
        return None;
    }
    let addr = z21::parse_addr(pkt, 6)?;
    let nibble = pkt[5] & 0x0F;
    let db2 = if nibble == 3 { 4 } else { nibble };
    let (speed, forward) = z21::decode_drive_from_loco_info(db2, pkt[8]);
    Some((addr, speed, forward))
}

fn parse_set_function(pkt: &[u8]) -> Option<(u16, u8, bool)> {
    if pkt.len() < 10 {
        return None;
    }
    let header = u16::from_le_bytes([pkt[2], pkt[3]]);
    if header != z21::HEADER_XBUS || pkt[4] != 0xE4 || pkt[5] != 0xF8 {
        return None;
    }
    let addr = z21::parse_addr(pkt, 6)?;
    let sw = pkt[8] >> 6;
    let func = pkt[8] & 0x3F;
    let on = sw == 1;
    Some((addr, func, on))
}

fn echo_loco(sock: &UdpSocket, from: SocketAddr, host: &dyn DriveHost, addr: u16) {
    let (speed, forward, functions) = host.loco_state(addr);
    let mut out = z21::WireBuf::new();
    if z21::encode_loco_info(&mut out, addr, speed, forward, 128, functions).is_ok() {
        let _ = sock.send_to(&out, from);
    }
}

/// In-memory host for tests.
#[derive(Default)]
pub struct Recorder {
    inner: Mutex<Inner>,
}

#[derive(Default)]
struct Inner {
    pub speed: Option<(u16, u8, bool)>,
    pub func: Option<(u16, u8, bool)>,
    states: std::collections::HashMap<u16, (u8, bool, u32)>,
}

impl Recorder {
    #[must_use]
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    #[must_use]
    pub fn last_speed(&self) -> Option<(u16, u8, bool)> {
        self.inner.lock().unwrap().speed
    }
}

impl DriveHost for Recorder {
    fn set_speed(&self, addr: u16, speed: u8, forward: bool, _steps: u8) {
        let mut g = self.inner.lock().unwrap();
        g.speed = Some((addr, speed, forward));
        let e = g.states.entry(addr).or_default();
        e.0 = speed;
        e.1 = forward;
    }

    fn set_function(&self, addr: u16, func: u8, on: bool) {
        let mut g = self.inner.lock().unwrap();
        g.func = Some((addr, func, on));
        let e = g.states.entry(addr).or_default();
        if on {
            e.2 |= 1 << func;
        } else {
            e.2 &= !(1 << func);
        }
    }

    fn loco_state(&self, addr: u16) -> (u8, bool, u32) {
        self.inner
            .lock()
            .unwrap()
            .states
            .get(&addr)
            .copied()
            .unwrap_or((0, true, 0))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::UdpSocket;
    use std::time::{Duration, Instant};

    #[test]
    fn listen_set_drive() {
        let host = Recorder::new();
        let srv = Server::listen("127.0.0.1:0", host.clone()).unwrap();
        let addr = srv.local_addr().unwrap();
        let cli = UdpSocket::bind("127.0.0.1:0").unwrap();
        cli.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
        let mut pkt = z21::WireBuf::new();
        z21::encode_get_serial(&mut pkt).unwrap();
        cli.send_to(&pkt, addr).unwrap();
        let mut buf = [0u8; 64];
        let _ = cli.recv_from(&mut buf).unwrap();
        pkt.clear();
        z21::encode_set_drive(&mut pkt, 3, 50, true, 3).unwrap();
        cli.send_to(&pkt, addr).unwrap();
        let deadline = Instant::now() + Duration::from_secs(2);
        while Instant::now() < deadline {
            if host.last_speed() == Some((3, 50, true)) {
                return;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        panic!("no SetSpeed: {:?}", host.last_speed());
    }
}
