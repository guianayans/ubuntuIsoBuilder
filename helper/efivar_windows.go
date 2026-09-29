//go:build windows

package main

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// Variáveis UEFI pela API do Windows (a mesma do EasyUEFI/bcdedit). Precisa de
// administrador e do privilégio SeSystemEnvironmentPrivilege ligado no processo.
var (
	k32          = syscall.NewLazyDLL("kernel32.dll")
	procGetFwVar = k32.NewProc("GetFirmwareEnvironmentVariableW")
	procSetFwVar = k32.NewProc("SetFirmwareEnvironmentVariableExW")
	advapi       = syscall.NewLazyDLL("advapi32.dll")
	procOpenTok  = advapi.NewProc("OpenProcessToken")
	procLookup   = advapi.NewProc("LookupPrivilegeValueW")
	procAdjust   = advapi.NewProc("AdjustTokenPrivileges")
	privOnce     sync.Once
	privErr      error
)

const efiGlobalGUIDWin = "{8BE4DF61-93CA-11D2-AA0D-00E098032B8C}"

func enableFirmwarePrivilege() error {
	privOnce.Do(func() {
		proc, _ := syscall.GetCurrentProcess()
		var tok syscall.Token
		if r, _, e := procOpenTok.Call(uintptr(proc), 0x20|0x08, uintptr(unsafe.Pointer(&tok))); r == 0 {
			privErr = e
			return
		}
		defer tok.Close()
		var tp struct {
			Count uint32
			Low   uint32
			High  int32
			Attr  uint32
		}
		name, _ := syscall.UTF16PtrFromString("SeSystemEnvironmentPrivilege")
		if r, _, e := procLookup.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&tp.Low))); r == 0 {
			privErr = e
			return
		}
		tp.Count, tp.Attr = 1, 2 // SE_PRIVILEGE_ENABLED
		r, _, e := procAdjust.Call(uintptr(tok), 0, uintptr(unsafe.Pointer(&tp)), 0, 0, 0)
		if r == 0 || e == syscall.Errno(1300) { // ERROR_NOT_ALL_ASSIGNED: sem administrador
			privErr = errors.New("é preciso abrir como administrador")
		}
	})
	return privErr
}

func uefiFirmware() bool {
	proc := k32.NewProc("GetFirmwareType")
	if proc.Find() != nil {
		return false
	}
	var t uint32 // 1 = BIOS, 2 = UEFI
	ok, _, _ := proc.Call(uintptr(unsafe.Pointer(&t)))
	return ok != 0 && t == 2
}

func efiSupported() bool { return fakeEFIDir() != "" || uefiFirmware() }

func efiGet(name string) ([]byte, error) {
	if fakeEFIDir() != "" {
		return fakeEFIGet(name)
	}
	if err := enableFirmwarePrivilege(); err != nil {
		return nil, err
	}
	n, _ := syscall.UTF16PtrFromString(name)
	g, _ := syscall.UTF16PtrFromString(efiGlobalGUIDWin)
	buf := make([]byte, 8192)
	r, _, e := procGetFwVar.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return nil, e
	}
	return buf[:r], nil
}

func efiSet(name string, data []byte) error {
	if fakeEFIDir() != "" {
		return fakeEFISet(name, data)
	}
	if err := enableFirmwarePrivilege(); err != nil {
		return err
	}
	n, _ := syscall.UTF16PtrFromString(name)
	g, _ := syscall.UTF16PtrFromString(efiGlobalGUIDWin)
	r, _, e := procSetFwVar.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(g)),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 7) // NV | BS | RT
	if r == 0 {
		return e
	}
	return nil
}

// BitLocker: com a ordem de boot mexida, o Windows poderia pedir a chave de
// recuperação se voltar a ligar; suspende a proteção só por um reinício.
func osBeforeInstallReboot() {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	_ = hidden("manage-bde.exe", "-protectors", "-disable", drive, "-RebootCount", "1").Run()
}

// a tarefa agendada que o painel cria para o modo --boot-usb
func osBootUSBCleanup() { _ = hidden("schtasks.exe", "/delete", "/tn", "isoBuilderFormat", "/f").Run() }

func osRebootNow() error {
	return hidden("shutdown.exe", "/r", "/t", "5", "/c", "isoBuilder: reiniciando pelo pendrive para instalar").Run()
}
