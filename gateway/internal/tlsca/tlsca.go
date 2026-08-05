// Package tlsca manages the local certificate authority used by the
// transparent-interception TLS door. It generates a self-signed CA,
// mints a leaf certificate for api.anthropic.com, and installs the CA
// into the macOS System keychain so Claude Code's TLS stack trusts it.
//
// The CA lives at ~/.sference/switch/ca/{ca.pem,ca-key.pem} with 0700
// permissions. The leaf cert is minted once and reused; the CA is the
// trust anchor.
package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	// CACommonName is the name shown in the System keychain.
	CACommonName = "Sference Switch Local CA"
	// LeafCommonName is the leaf cert's CN (the intercepted host).
	LeafCommonName = "api.anthropic.com"
	// LeafDNSNames are the SANs on the leaf cert.
	LeafDNSNames = "api.anthropic.com"
)

// Paths returns the CA and leaf cert paths under the config dir.
func Paths(configDir string) (caCert, caKey, leafCert, leafKey string) {
	dir := filepath.Join(configDir, "ca")
	return filepath.Join(dir, "ca.pem"),
		filepath.Join(dir, "ca-key.pem"),
		filepath.Join(dir, "leaf.pem"),
		filepath.Join(dir, "leaf-key.pem")
}

// Ensure generates the CA and leaf cert if they don't exist. Returns the
// paths to the leaf cert and key (what the TLS door loads).
func Ensure(configDir string) (leafCert, leafKey string, err error) {
	caCert, caKey, leafCert, leafKey := Paths(configDir)
	if fileExists(caCert) && fileExists(caKey) && fileExists(leafCert) && fileExists(leafKey) {
		return leafCert, leafKey, nil
	}
	if err := os.MkdirAll(filepath.Dir(caCert), 0o700); err != nil {
		return "", "", err
	}
	// Generate CA
	caKeyPEM, caCertPEM, err := generateCA()
	if err != nil {
		return "", "", fmt.Errorf("generate CA: %w", err)
	}
	if err := writeFile(caKey, caKeyPEM, 0o600); err != nil {
		return "", "", err
	}
	if err := writeFile(caCert, caCertPEM, 0o644); err != nil {
		return "", "", err
	}
	// Mint leaf
	leafKeyPEM, leafCertPEM, err := mintLeaf(caCertPEM, caKeyPEM)
	if err != nil {
		return "", "", fmt.Errorf("mint leaf: %w", err)
	}
	if err := writeFile(leafKey, leafKeyPEM, 0o600); err != nil {
		return "", "", err
	}
	if err := writeFile(leafCert, leafCertPEM, 0o644); err != nil {
		return "", "", err
	}
	return leafCert, leafKey, nil
}

// InstallCAToSystemKeychain adds the CA to the macOS System keychain as a
// trusted root. Requires sudo.
func InstallCAToSystemKeychain(configDir string) error {
	caCert, _, _, _ := Paths(configDir)
	if !fileExists(caCert) {
		return fmt.Errorf("CA not found at %s; run 'sference-switch tls setup' first", caCert)
	}
	cmd := exec.Command("security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain",
		caCert)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security add-trusted-cert: %w", err)
	}
	return nil
}

// RemoveCAFromSystemKeychain removes the CA from the System keychain.
// Requires sudo.
func RemoveCAFromSystemKeychain(configDir string) error {
	caCert, _, _, _ := Paths(configDir)
	if !fileExists(caCert) {
		return nil // already gone
	}
	cmd := exec.Command("security", "delete-certificate",
		"-c", CACommonName,
		"/Library/Keychains/System.keychain")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security delete-certificate: %w", err)
	}
	return nil
}

// generateCA creates a self-signed ECDSA CA certificate.
func generateCA() (keyPEM, certPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   CACommonName,
			Organization: []string{"Sference"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return keyPEM, certPEM, nil
}

// mintLeaf creates a leaf certificate for api.anthropic.com signed by the CA.
func mintLeaf(caCertPEM, caKeyPEM []byte) (keyPEM, certPEM []byte, err error) {
	caCertBlock, _ := pem.Decode(caCertPEM)
	if caCertBlock == nil {
		return nil, nil, fmt.Errorf("decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return nil, nil, fmt.Errorf("decode CA key PEM")
	}
	caKeyAny, err := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	caKey, ok := caKeyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("CA key is not ECDSA")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   LeafCommonName,
			Organization: []string{"Sference"},
		},
		DNSNames:    []string{LeafDNSNames},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return keyPEM, certPEM, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
