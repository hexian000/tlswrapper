package tlswrapper

import (
	"crypto/x509"
	"os"
	"testing"
)

func TestReadWriteKeyPairRoundTrip(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	gen, err := makeKeyGenerator("ecdsa", 256)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, key, err := gen()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := newCertificate(nil, nil, "peer.example.com", pubKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeKeyPair("peer", certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	cert, parsedKey, err := readKeyPair("peer")
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "peer.example.com" {
		t.Fatalf("CommonName = %q, want %q", cert.Subject.CommonName, "peer.example.com")
	}
	if parsedKey == nil {
		t.Fatal("expected parsed private key")
	}
}

func TestGenCertsCreatesSignedCertificates(t *testing.T) {
	dir := t.TempDir()
	chdirTemp(t, dir)
	if code := genCerts(&AppFlags{GenCerts: "ca", ServerName: "ca.example.com", KeyType: "ed25519"}); code != 0 {
		t.Fatalf("genCerts(ca) = %d, want 0", code)
	}
	if code := genCerts(&AppFlags{GenCerts: "leaf-a,leaf-b", Sign: "ca", ServerName: "leaf.example.com", KeyType: "ecdsa", KeySize: 256}); code != 0 {
		t.Fatalf("genCerts(leaves) = %d, want 0", code)
	}
	caCert, _, err := readKeyPair("ca")
	if err != nil {
		t.Fatal(err)
	}
	leafCert, _, err := readKeyPair("leaf-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readKeyPair("leaf-b"); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "leaf.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca-cert.pem", "ca-key.pem", "leaf-a-cert.pem", "leaf-a-key.pem", "leaf-b-cert.pem", "leaf-b-key.pem"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("Stat(%q): %v", name, err)
		}
	}
}
