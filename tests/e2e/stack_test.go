//go:build e2e

package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stack struct {
	key        string
	signingKey []byte
}

func run(t *testing.T, program string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), program, args...)
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", program, args[0], err, diagnostics.String())
	}
	return string(out)
}

func startStack(t *testing.T) stack {
	t.Helper()
	dir := t.TempDir()
	cluster := "nzb-e2e-" + strings.ToLower(rand.Text())
	t.Log("kind cluster:", cluster)
	t.Setenv("KUBECONFIG", filepath.Join(dir, "kubeconfig"))
	t.Setenv("KIND_EXPERIMENTAL_PROVIDER", "docker")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "kind", "delete", "cluster", "--name", cluster).CombinedOutput(); err != nil {
			t.Errorf("delete kind cluster: %v\n%s", err, out)
		}
	})
	run(t, "kind", "create", "cluster", "--name", cluster, "--kubeconfig", os.Getenv("KUBECONFIG"), "--wait", "3m")
	t.Cleanup(func() {
		if t.Failed() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out, _ := exec.CommandContext(ctx, "kubectl", "get", "events", "--sort-by=.metadata.creationTimestamp").CombinedOutput()
			t.Logf("cluster events:\n%s", out)
		}
	})

	// Export directly into this cluster so concurrent checkouts never share mutable image tags.
	for _, image := range []struct{ tag, stage string }{{"nzb-proxy-builder:e2e", "build"}, {"nzb-proxy:e2e", ""}} {
		archive := filepath.Join(dir, "image.tar")
		args := []string{"buildx", "build", "--provenance=false", "-t", image.tag, "--output", "type=docker,dest=" + archive}
		if image.stage != "" {
			args = append(args, "--target", image.stage)
		}
		run(t, "docker", append(args, "../..")...)
		run(t, "kind", "load", "image-archive", archive, "--name", cluster)
	}

	key := make([]byte, 16)
	rand.Read(key)
	s := stack{key: hex.EncodeToString(key), signingKey: make([]byte, 32)}
	rand.Read(s.signingKey)
	keyFile := filepath.Join(dir, "signing-key")
	if err := os.WriteFile(keyFile, s.signingKey, 0600); err != nil {
		t.Fatal(err)
	}
	run(t, "kubectl", "create", "secret", "generic", "nzb-proxy", "--from-literal=api-key="+s.key, "--from-file=signing-key="+keyFile)
	tls := filepath.Join(dir, "tls")
	writeCertificate(t, tls)
	run(t, "kubectl", "create", "secret", "generic", "fixture-tls", "--from-file="+tls)
	run(t, "kubectl", "create", "configmap", "fixture-ca", "--from-file="+filepath.Join(tls, "cert.pem"))
	run(t, "kubectl", "create", "configmap", "fixture-source", "--from-file=fixture.go")
	run(t, "kubectl", "apply", "-f", "fixture.yaml")
	for _, app := range []struct{ name, version, port string }{
		{"prowlarr", "2.4.0.5391", "9696"}, {"radarr", "6.2.0.10390", "7878"},
	} {
		prefix := strings.ToUpper(app.name) + "__AUTH__"
		probe := fmt.Sprintf(`{"spec":{"containers":[{"name":%q,"readinessProbe":{"httpGet":{"path":"/ping","port":%s},"periodSeconds":2}}]}}`, app.name, app.port)
		run(t, "kubectl", "run", app.name, "--image=ghcr.io/home-operations/"+app.name+":"+app.version, "--restart=Never", "--port="+app.port,
			"--override-type=strategic", "--overrides="+probe,
			"--env="+prefix+"APIKEY="+s.key, "--env="+prefix+"METHOD=External")
		run(t, "kubectl", "expose", "pod", app.name, "--port="+app.port)
	}
	run(t, "kubectl", "wait", "--for=condition=Ready", "pod/prowlarr", "pod/radarr", "pod/fixture", "--timeout=4m")
	return s
}

// kubectl selects a free loopback port. A pod replacement needs a new forward.
func forward(t *testing.T, service, port string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "kubectl", "port-forward", "--address=127.0.0.1", "service/"+service, ":"+port)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// kubectl logs individual streams and expected disconnects during rollouts.
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			log := diagnostics.String()
			t.Logf("port-forward %s: %s", service, log[max(0, len(log)-2000):])
		}
	})
	reader := bufio.NewReader(out)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("port-forward %s: %v", service, err)
	}
	var local int
	if _, err := fmt.Sscanf(line, "Forwarding from 127.0.0.1:%d", &local); err != nil {
		t.Fatal(line, err)
	}
	go io.Copy(io.Discard, reader)
	return fmt.Sprintf("http://127.0.0.1:%d", local)
}

func writeCertificate(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"fixture"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(48 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
}
