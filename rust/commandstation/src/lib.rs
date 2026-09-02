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
}

/// No-op station. CV ops return [`Unsupported`].
#[doc = "experimental"]
#[derive(Default)]
pub struct Stub;

impl Station for Stub {
    fn set_speed(&mut self, _addr: u16, _speed: u8, _forward: bool, _steps: u8) -> Result<(), Unsupported> {
        Ok(())
    }
    fn get_speed(&mut self, _addr: u16) -> Result<(u8, bool), Unsupported> {
        Ok((0, true))
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
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stub_cv_unsupported() {
        let mut s = Stub;
        assert_eq!(s.read_cv(8).unwrap_err(), Unsupported);
    }
}
