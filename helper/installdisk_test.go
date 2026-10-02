//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKexecCmdlineMatchesUSB(t *testing.T) {
	c := kexecCmdline("https://iso.exemplo.com/f/tok/iso", false)
	// mesma seed embutida do pendrive + url= e ip=dhcp
	for _, want := range []string{"autoinstall", "ds=nocloud;s=/cdrom/nocloud/", "url=https://iso.exemplo.com/f/tok/iso", "ip=dhcp", "boot=casper"} {
		if !strings.Contains(c, want) {
			t.Fatalf("cmdline sem %q: %s", want, c)
		}
	}
	if !strings.HasSuffix(c, "---") {
		t.Fatalf("cmdline não termina em ---: %s", c)
	}
	if strings.Contains(c, "toram") {
		t.Fatalf("toram=false não deveria incluir toram: %s", c)
	}
	// com toram: a flag aparece e a linha continua terminando em ---
	ct := kexecCmdline("https://iso.exemplo.com/f/tok/iso", true)
	if !strings.Contains(ct, " toram ") || !strings.HasSuffix(ct, "---") {
		t.Fatalf("toram=true deveria incluir ' toram ' e terminar em ---: %s", ct)
	}
}

func TestRunInstallDiskFlow(t *testing.T) {
	bootCountdownStep, bootCountdownN = 15*time.Millisecond, 2
	t.Setenv("UAB_FAKE_DISK", "1")
	kexOut := t.TempDir() + "/kexec.txt"
	t.Setenv("UAB_FAKE_KEXEC", kexOut)

	kernel, initrd := []byte(strings.Repeat("K", 200000)), []byte(strings.Repeat("I", 300000))
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

	var mu sync.Mutex
	var states []string
	cancelAt := -1
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/kernel"):
			w.Write(kernel)
			return
		case strings.HasSuffix(r.URL.Path, "/initrd"):
			w.Write(initrd)
			return
		}
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		states = append(states, m["state"])
		c := cancelAt >= 0 && len(states) >= cancelAt
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"cancel": c})
	}))
	defer ts.Close()

	code := runInstallDisk(ts.URL+"/f/tok", sum(kernel), sum(initrd), time.Minute, ts.URL+"/f/tok/report")
	if code != 0 {
		t.Fatalf("saída %d, estados %v", code, states)
	}
	joined := strings.Join(states, ",")
	if !strings.Contains(joined, "baixando") || !strings.Contains(joined, "preparando") || !strings.HasSuffix(joined, "reiniciando") {
		t.Fatalf("estados: %s", joined)
	}
	got, _ := os.ReadFile(kexOut)
	if !strings.Contains(string(got), "url="+ts.URL+"/f/tok/image.iso") || !strings.Contains(string(got), "autoinstall") {
		t.Fatalf("kexec preparado errado: %s", got)
	}

	// hash errado do kernel: erro, sem kexec
	_ = os.Remove(kexOut)
	mu.Lock()
	states = nil
	mu.Unlock()
	if code := runInstallDisk(ts.URL+"/f/tok", sum([]byte("outro")), sum(initrd), time.Minute, ts.URL+"/f/tok/report"); code != 1 {
		t.Fatalf("hash errado deveria falhar: %d", code)
	}
	if _, err := os.Stat(kexOut); err == nil {
		t.Fatal("kexec preparado apesar do hash errado")
	}

	// cancelado no meio: sem reiniciar
	mu.Lock()
	states, cancelAt = nil, 1
	mu.Unlock()
	if code := runInstallDisk(ts.URL+"/f/tok", sum(kernel), sum(initrd), time.Minute, ts.URL+"/f/tok/report"); code != 5 {
		t.Fatalf("cancelar deveria dar 5: %d, %v", code, states)
	}
}

func TestSafeHex64(t *testing.T) {
	if !safeHex64(strings.Repeat("a", 64)) || safeHex64(strings.Repeat("a", 63)) || safeHex64(strings.Repeat("z", 64)) {
		t.Fatal("safeHex64")
	}
}
