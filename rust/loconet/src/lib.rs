//! Experimental LocoNet gateway. The Go `loconet.Gateway` is the
//! implementation; this crate exists so a future Rust consumer can depend on
//! the same module layout.
#![allow(missing_docs)]

/// TCP framing for downstream listeners.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    Binary,
    Ascii,
}

/// Frame-fanout hub. Downstream `listen` is not implemented yet.
#[doc = "experimental"]
pub struct Gateway;

impl Gateway {
    #[must_use]
    pub fn new() -> Self {
        Self
    }

    /// Bind a downstream listener. Returns an error until a Rust consumer
    /// needs a real implementation.
    pub fn listen(&self, _bind: &str, _mode: Mode) -> Result<(), &'static str> {
        Err("experimental: loconet gateway is implemented in Go")
    }
}

impl Default for Gateway {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn new_gateway_listen_is_experimental() {
        let gw = Gateway::new();
        assert!(gw.listen("127.0.0.1:0", Mode::Binary).is_err());
    }
}
