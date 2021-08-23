package workergrpc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"google.golang.org/grpc/credentials"
)

func MTLSServerCredentials(clientCACert, serverCert, serverKey []byte) (credentials.TransportCredentials, error) {
	// Load client CA cert
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(clientCACert) {
		return nil, fmt.Errorf("failed adding client CA cert from PEM")
	}
	// Load server key pair
	cert, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		Certificates: []tls.Certificate{cert},
	}), nil
}

func MTLSClientCredentials(serverCACert, clientCert, clientKey []byte) (credentials.TransportCredentials, error) {
	// Load server CA cert
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(serverCACert) {
		return nil, fmt.Errorf("failed adding server CA cert from PEM")
	}
	// Load client key pair
	cert, err := tls.X509KeyPair(clientCert, clientKey)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
	}), nil
}

type GenerateCertificateConfig struct {
	SignerCert []byte
	SignerKey  []byte
	OU         string
	// If true, this key can sign others and is marked as a CA. CA certs are only
	// used for signing and verification, not directly for server/client auth.
	// TODO(cretz): Intentionally not separating signer from CA, but could change
	// to have non-CA intermediates if needed
	CA bool
	// The IP or DNS name used by the server. If this is non-empty, the
	// certificate will be a server certificate for server auth. If this is empty,
	// the certificate will be a client certificate for client auth. This field
	// is ignored if CA is true.
	ServerHost string
}

// GenerateCertificate ... P-256 ... expires in 365 days
func GenerateCertificate(config GenerateCertificateConfig) (certPEM, keyPEM []byte, err error) {
	// // Create template for P-256 cert
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	cert := &x509.Certificate{
		// IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		// Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  config.CA,
	}
	if cert.SerialNumber, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)); err != nil {
		return nil, nil, fmt.Errorf("generating serial number: %w", err)
	}
	if config.OU != "" {
		cert.Subject.OrganizationalUnit = []string{config.OU}
	}
	if config.CA {
		cert.KeyUsage |= x509.KeyUsageCertSign
	} else if config.ServerHost != "" {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		// Try to parse server as IP
		cert.Subject.CommonName = config.ServerHost
		if ip := net.ParseIP(config.ServerHost); ip != nil {
			cert.IPAddresses = []net.IP{ip}
		} else {
			cert.DNSNames = []string{config.ServerHost}
		}
	} else {
		cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	// Load signer pair or use self signed
	parentCert, parentPriv := cert, priv
	if len(config.SignerCert) > 0 || len(config.SignerKey) > 0 {
		if len(config.SignerCert) == 0 || len(config.SignerKey) == 0 {
			return nil, nil, fmt.Errorf("both signer cert and key must be absent or present")
		}
		// Load cert
		if block, _ := pem.Decode(config.SignerCert); block == nil {
			return nil, nil, fmt.Errorf("failed reading cert PEM")
		} else if parentCert, err = x509.ParseCertificate(block.Bytes); err != nil {
			return nil, nil, fmt.Errorf("parsing cert: %w", err)
		}
		// Load private key
		if block, _ := pem.Decode(config.SignerKey); block == nil {
			return nil, nil, fmt.Errorf("failed reading key PEM")
		} else if parentPrivIface, err := x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
			return nil, nil, fmt.Errorf("parsing key: %w", err)
		} else if parentPriv, _ = parentPrivIface.(*ecdsa.PrivateKey); parentPriv == nil {
			return nil, nil, fmt.Errorf("unexpected private key type %T", parentPrivIface)
		}
	}
	// Create the cert
	certBytes, err := x509.CreateCertificate(rand.Reader, cert, parentCert, &priv.PublicKey, parentPriv)
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate: %w", err)
	}
	// Serialize to PEM and return
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})
	return certPEM, keyPEM, nil
}
