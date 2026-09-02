//! Experimental `Station` client surface aligned with Go `commandstation`.
#![allow(missing_docs)]

use core::fmt;

/// Returned by operations a protocol cannot express (WiThrottle CV).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Unsupported;

impl fmt::Display for Unsupported {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("operation not supported")
    }
}

impl std::error::Error for Unsupported {}

/// Synchronous command-station client.
pub trait Station {
    fn set_speed(
        &mut self,
        addr: u16,
        speed: u8,
        forward: bool,
        steps: u8,
    ) -> Result<(), Unsupported>;
    fn get_speed(&mut self, addr: u16) -> Result<(u8, bool), Unsupported>;
    fn send_fn(&mut self, addr: u16, func: u8, on: bool) -> Result<(), Unsupported>;
    fn read_cv(&mut self, cv: u16) -> Result<u8, Unsupported>;
    fn write_cv(&mut self, cv: u16, value: u8) -> Result<(), Unsupported>;
    /// Per-loco e-stop. Protocols without a spec e-stop send speed 0.
    fn emergency_stop(&mut self, addr: u16, forward: bool) -> Result<(), Unsupported>;
}

/// No-op station. CV ops return [`Unsupported`]. E-stop falls back to speed 0.
#[doc = "experimental"]
pub struct Stub {
    speed: u8,
    forward: bool,
}

impl Default for Stub {
    fn default() -> Self {
        Self {
            speed: 0,
            forward: true,
        }
    }
}

impl Station for Stub {
    fn set_speed(
        &mut self,
        _addr: u16,
        speed: u8,
        forward: bool,
        _steps: u8,
    ) -> Result<(), Unsupported> {
        self.speed = speed;
        self.forward = forward;
        Ok(())
    }
    fn get_speed(&mut self, _addr: u16) -> Result<(u8, bool), Unsupported> {
        Ok((self.speed, self.forward))
    }
    fn send_fn(&mut self, _addr: u16, _func: u8, _on: bool) -> Result<(), Unsupported> {
        Ok(())
    }
    fn read_cv(&mut self, _cv: u16) -> Result<u8, Unsupported> {
        Err(Unsupported)
    }
    fn write_cv(&mut self, _cv: u16, _value: u8) -> Result<(), Unsupported> {
        Err(Unsupported)
    }
    fn emergency_stop(&mut self, addr: u16, forward: bool) -> Result<(), Unsupported> {
        self.set_speed(addr, 0, forward, 128)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stub_cv_unsupported() {
        let mut s = Stub::default();
        assert_eq!(s.read_cv(8).unwrap_err(), Unsupported);
    }

    #[test]
    fn stub_estop_is_speed_zero() {
        let mut s = Stub::default();
        s.set_speed(3, 50, true, 128).unwrap();
        s.emergency_stop(3, false).unwrap();
        assert_eq!(s.get_speed(3).unwrap(), (0, false));
    }
}
