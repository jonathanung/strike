use std::io;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::WebPkiServerVerifier;
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{ClientConfig, DigitallySignedStruct, Error, RootCertStore, SignatureScheme};
use sha2::{Digest, Sha256};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};
use tokio::net::TcpStream;
use tokio::time::{timeout, Duration};
use tokio_rustls::client::TlsStream;
use tokio_rustls::TlsConnector;

use crate::constant_eq;
use crate::error::GatewayError;
use crate::install_crypto;

pub fn fingerprint(der: &[u8]) -> String {
    hex::encode(Sha256::digest(der))
}

pub enum Stream {
    Plain(TcpStream),
    Tls(TlsStream<TcpStream>),
}

impl AsyncRead for Stream {
    fn poll_read(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<io::Result<()>> {
        match self.get_mut() {
            Stream::Plain(s) => Pin::new(s).poll_read(cx, buf),
            Stream::Tls(s) => Pin::new(s).poll_read(cx, buf),
        }
    }
}

impl AsyncWrite for Stream {
    fn poll_write(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        match self.get_mut() {
            Stream::Plain(s) => Pin::new(s).poll_write(cx, buf),
            Stream::Tls(s) => Pin::new(s).poll_write(cx, buf),
        }
    }

    fn poll_flush(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        match self.get_mut() {
            Stream::Plain(s) => Pin::new(s).poll_flush(cx),
            Stream::Tls(s) => Pin::new(s).poll_flush(cx),
        }
    }

    fn poll_shutdown(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        match self.get_mut() {
            Stream::Plain(s) => Pin::new(s).poll_shutdown(cx),
            Stream::Tls(s) => Pin::new(s).poll_shutdown(cx),
        }
    }
}

pub async fn connect(
    host: &str,
    port: u16,
    tls: bool,
    pin: Option<&str>,
) -> Result<Stream, GatewayError> {
    let addr = format!("{host}:{port}");
    let tcp = timeout(Duration::from_secs(5), TcpStream::connect(&addr))
        .await
        .map_err(|_| GatewayError::Unreachable {
            detail: "timed out".into(),
        })?
        .map_err(|err| GatewayError::Unreachable {
            detail: io_detail(&err),
        })?;
    let _ = tcp.set_nodelay(true);
    if !tls {
        return Ok(Stream::Plain(tcp));
    }
    install_crypto();
    let captured = Arc::new(Mutex::new(None::<String>));
    let config = client_config(pin.map(|s| s.to_string()), captured.clone())?;
    let name = ServerName::try_from(host.to_string()).map_err(|_| GatewayError::InvalidLink {
        detail: "TLS name is not a DNS name or IP".into(),
    })?;
    let connector = TlsConnector::from(config);
    match timeout(Duration::from_secs(5), connector.connect(name, tcp)).await {
        Ok(Ok(tls)) => Ok(Stream::Tls(tls)),
        Ok(Err(_)) | Err(_) => {
            let fp = captured.lock().ok().and_then(|g| g.clone());
            if let (Some(pin), Some(fp)) = (pin, fp.as_deref()) {
                if !constant_eq(&normalize_fp(pin), &normalize_fp(fp)) {
                    return Err(GatewayError::PinMismatch {
                        fingerprint: normalize_fp(fp),
                    });
                }
                return Err(GatewayError::Unreachable {
                    detail: "pinned certificate did not finish the handshake".into(),
                });
            }
            if let Some(fp) = fp {
                return Err(GatewayError::BadCert {
                    fingerprint: normalize_fp(&fp),
                });
            }
            Err(GatewayError::Unreachable {
                detail: "tls handshake failed".into(),
            })
        }
    }
}

fn client_config(
    pin: Option<String>,
    captured: Arc<Mutex<Option<String>>>,
) -> Result<Arc<ClientConfig>, GatewayError> {
    let mut roots = RootCertStore::empty();
    roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
    let webpki = WebPkiServerVerifier::builder(Arc::new(roots))
        .build()
        .map_err(|err| GatewayError::Unreachable {
            detail: format!("tls roots: {err}"),
        })?;
    let verifier = Arc::new(PinVerifier {
        pin,
        captured,
        webpki,
    });
    let config = ClientConfig::builder()
        .dangerous()
        .with_custom_certificate_verifier(verifier)
        .with_no_client_auth();
    Ok(Arc::new(config))
}

#[derive(Debug)]
struct PinVerifier {
    pin: Option<String>,
    captured: Arc<Mutex<Option<String>>>,
    webpki: Arc<WebPkiServerVerifier>,
}

impl ServerCertVerifier for PinVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, Error> {
        let fp = fingerprint(end_entity.as_ref());
        if let Ok(mut slot) = self.captured.lock() {
            *slot = Some(fp.clone());
        }
        if let Some(pin) = &self.pin {
            if constant_eq(&normalize_fp(pin), &fp) {
                return Ok(ServerCertVerified::assertion());
            }
            return Err(Error::General("certificate pin mismatch".into()));
        }
        self.webpki
            .verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now)
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, Error> {
        self.webpki.verify_tls12_signature(message, cert, dss)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, Error> {
        self.webpki.verify_tls13_signature(message, cert, dss)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.webpki.supported_verify_schemes()
    }
}

pub fn normalize_fp(fp: &str) -> String {
    fp.chars()
        .filter(|c| c.is_ascii_hexdigit())
        .flat_map(|c| c.to_lowercase())
        .collect()
}

fn io_detail(err: &io::Error) -> String {
    match err.kind() {
        io::ErrorKind::ConnectionRefused => "connection refused".into(),
        io::ErrorKind::TimedOut => "timed out".into(),
        io::ErrorKind::NotFound => "host not found".into(),
        _ => "could not connect".into(),
    }
}
