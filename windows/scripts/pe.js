// ubuntu-autoinstall-builder (Windows) — instalação no Windows PE (passo windowsPE).
//
// O WinPE do Setup do Windows NÃO tem PowerShell nem findstr, mas tem cscript (JScript),
// WMI (Win32_DiskDrive), diskpart, dism e bcdboot. Este script:
//   1. acha a mídia de instalação (sources\install.wim/.esd/.swm) e o autounattend.xml
//      deste build;
//   2. escolhe o MAIOR disco interno que não seja USB/removível e que não contenha a mídia
//      nem o arquivo de respostas (nunca apaga o pendrive);
//   3. particiona (GPT/UEFI ou MBR/BIOS), aplica a imagem da edição com o dism,
//      cria o boot com o bcdboot e copia o arquivo de respostas para
//      <Windows>\Panther\unattend.xml (os passos specialize/oobeSystem continuam dele);
//   4. reinicia. Se algo falhar ANTES de apagar, mostra o motivo e devolve o controle ao
//      Setup normal (interativo: o técnico escolhe o disco). Nada é apagado sem disco elegível.
//
// O gerador (web/app/winconfig.py) coloca antes deste arquivo a linha
//   var UAB = {build: "...", image: "Windows 11 Pro", minGB: 24};
// e converte caracteres não ASCII em \uXXXX (o arquivo passa por "find" e "echo" do cmd).

var sh = new ActiveXObject("WScript.Shell");
var fso = new ActiveXObject("Scripting.FileSystemObject");
var DIR = "X:\\uab";
var LOG = DIR + "\\pe.log";
var MARK = "uab-build:" + UAB.build;

function log(s) {
    try {
        var f = fso.OpenTextFile(LOG, 8, true);
        f.WriteLine(new Date().toString() + "  " + s);
        f.Close();
    } catch (e) { }
}

function readText(p) {
    try {
        if (!fso.FileExists(p)) return "";
        var f = fso.OpenTextFile(p, 1, false, 0);
        var t = f.AtEndOfStream ? "" : f.ReadAll();
        f.Close();
        return t;
    } catch (e) { return ""; }
}

function writeText(p, t, unicode) {
    var f = fso.CreateTextFile(p, true, !!unicode);
    f.Write(t);
    f.Close();
}

// Roda um comando escondido, espera e devolve {code, out}
var seq = 0;
function run(cmd) {
    var out = DIR + "\\out" + (seq++) + ".txt";
    log("> " + cmd);
    var code = sh.Run("cmd.exe /c " + cmd + " >\"" + out + "\" 2>&1", 0, true);
    var t = readText(out);
    log("  (código " + code + ") " + t.replace(/\s+$/, "").split(/\r?\n/).slice(-15).join(" | "));
    return { code: code, out: t };
}

// Mostra uma mensagem ao técnico (Bloco de Notas por cima do Setup) e espera fechar
function tell(msg) {
    var p = DIR + "\\LEIA-ME.txt";
    writeText(p, msg + "\r\n\r\n(Registro técnico: " + LOG + ")\r\n", true);
    log("MENSAGEM: " + msg.replace(/\r?\n/g, " "));
    sh.Run("notepad.exe \"" + p + "\"", 1, true);
}

function fail(msg, erased) {
    tell("ISO Builder - instalação automática do Windows\r\n\r\n" + msg + "\r\n\r\n" +
        (erased ? "O disco escolhido já tinha sido apagado. " : "Nenhum disco foi apagado. ") +
        "\r\nAo fechar esta janela, o instalador normal do Windows continua\r\ne você pode escolher o disco manualmente.");
    WScript.Quit(1);
}

function lettersInUse() {
    var used = {};
    var e = new Enumerator(fso.Drives);
    for (; !e.atEnd(); e.moveNext()) used[e.item().DriveLetter.toUpperCase()] = true;
    return used;
}

function readyRoots() {
    var r = [];
    var e = new Enumerator(fso.Drives);
    for (; !e.atEnd(); e.moveNext()) {
        var d = e.item();
        try { if (d.IsReady) r.push(d.DriveLetter.toUpperCase()); } catch (x) { }
    }
    return r;
}

// ------------------------------------------------------------------ 1. mídia e respostas
log("=== ISO Builder: instalação no WinPE (build " + UAB.build + ") ===");
var roots = readyRoots();
log("unidades: " + roots.join(" "));

var media = null;
for (var i = 0; i < roots.length && !media; i++) {
    var s = roots[i] + ":\\sources\\";
    if (fso.FileExists(s + "install.wim")) media = { letter: roots[i], file: s + "install.wim" };
    else if (fso.FileExists(s + "install.esd")) media = { letter: roots[i], file: s + "install.esd" };
    else if (fso.FileExists(s + "install.swm")) media = { letter: roots[i], file: s + "install.swm", swm: s + "install*.swm" };
}
if (!media) fail("Não encontrei a imagem do Windows (sources\\install.wim, .esd ou .swm) em nenhuma unidade.", false);
log("mídia: " + media.file);

var answer = null, cands = [];
try { cands.push(sh.RegRead("HKLM\\SYSTEM\\Setup\\UnattendFile")); } catch (e) { }  // Ventoy e /unattend:
for (var i = 0; i < roots.length; i++) {
    cands.push(roots[i] + ":\\autounattend.xml");
    cands.push(roots[i] + ":\\unattend.xml");
}
cands.push("X:\\autounattend.xml", "X:\\unattend.xml", "X:\\Windows\\Panther\\unattend.xml",
    "X:\\Windows\\Panther\\autounattend.xml", "X:\\Windows\\System32\\autounattend.xml");
for (var i = 0; i < cands.length && !answer; i++) {
    if (cands[i] && readText(cands[i]).indexOf(MARK) >= 0) answer = cands[i];
}
if (!answer) fail("Não encontrei o arquivo de respostas (autounattend.xml) deste build.", false);
log("respostas: " + answer);
var answerLetter = answer.charAt(1) === ":" ? answer.charAt(0).toUpperCase() : "";

// ------------------------------------------------------------------ 2. já instalado por este pendrive?
// O UEFI às vezes volta a iniciar pelo pendrive depois da instalação: sem esta
// checagem o PC seria apagado e reinstalado em laço.
for (var i = 0; i < roots.length; i++) {
    var bid = readText(roots[i] + ":\\ProgramData\\uab\\build-id").replace(/\s+/g, "");
    if (bid && bid === UAB.build) {
        tell("ISO Builder - o Windows JÁ FOI INSTALADO neste computador por este pendrive.\r\n\r\n" +
            "Provavelmente o computador iniciou de novo pelo pendrive. Tire o pendrive e reinicie:\r\n" +
            "o Windows instalado continua a configuração sozinho.\r\n\r\n" +
            "Se você quer mesmo APAGAR e instalar de novo, feche esta janela.\r\n" +
            "Para não apagar nada, desligue o computador agora (botão de energia).");
        log("reinstalação confirmada pelo técnico (janela fechada)");
        break;
    }
}

// ------------------------------------------------------------------ 3. escolha do disco
var wmi;
try { wmi = GetObject("winmgmts:{impersonationLevel=impersonate}!\\\\.\\root\\cimv2"); }
catch (e) { fail("O WMI do Windows PE não respondeu (" + e.message + ").", false); }

function lettersOfDisk(dev) {
    var ls = [];
    try {
        var parts = new Enumerator(wmi.ExecQuery("ASSOCIATORS OF {Win32_DiskDrive.DeviceID='" +
            dev.replace(/\\/g, "\\\\") + "'} WHERE AssocClass=Win32_DiskDriveToDiskPartition"));
        for (; !parts.atEnd(); parts.moveNext()) {
            var lds = new Enumerator(wmi.ExecQuery("ASSOCIATORS OF {Win32_DiskPartition.DeviceID='" +
                parts.item().DeviceID + "'} WHERE AssocClass=Win32_LogicalDiskToPartition"));
            for (; !lds.atEnd(); lds.moveNext()) ls.push(String(lds.item().DeviceID).charAt(0).toUpperCase());
        }
    } catch (e) { log("aviso: letras do disco " + dev + ": " + e.message); }
    return ls;
}

var best = null, seen = [];
var disks = new Enumerator(wmi.ExecQuery("SELECT * FROM Win32_DiskDrive"));
for (; !disks.atEnd(); disks.moveNext()) {
    var d = disks.item();
    var idx = d.Index, size = parseFloat(d.Size || "0"), gb = Math.floor(size / 1e9);
    var itf = String(d.InterfaceType || ""), mt = String(d.MediaType || ""), pnp = String(d.PNPDeviceID || "");
    var ls = lettersOfDisk(d.DeviceID);
    var why = "";
    if (/^USB/i.test(itf) || /^USBSTOR\\/i.test(pnp) || /UASPSTOR/i.test(pnp)) why = "USB";
    else if (/1394/i.test(itf)) why = "FireWire";
    else if (/removable|remov|external|extern/i.test(mt)) why = "removível/externo (" + mt + ")";
    else if (media && indexOfArr(ls, media.letter) >= 0) why = "contém a mídia de instalação";
    else if (answerLetter && indexOfArr(ls, answerLetter) >= 0) why = "contém o arquivo de respostas";
    else if (size < UAB.minGB * 1e9) why = "pequeno demais (mínimo " + UAB.minGB + " GB)";
    var desc = "disco " + idx + ": " + (d.Model || "?") + ", " + gb + " GB, " + itf + ", " + mt + (ls.length ? ", unidades " + ls.join("") : "");
    log(desc + (why ? " -> ignorado: " + why : " -> elegível"));
    seen.push(desc + (why ? " (ignorado: " + why + ")" : ""));
    if (!why && (!best || size > best.size || (size === best.size && idx < best.index)))
        best = { index: idx, size: size, gb: gb, model: String(d.Model || "") };
}

function indexOfArr(a, v) { for (var i = 0; i < a.length; i++) if (a[i] === v) return i; return -1; }

if (!best) fail("Nenhum disco interno elegível para instalar o Windows (o pendrive e discos USB nunca são usados).\r\n\r\nDiscos encontrados:\r\n  " +
    (seen.length ? seen.join("\r\n  ") : "(nenhum)"), false);
log("ESCOLHIDO: disco " + best.index + " (" + best.model + ", " + best.gb + " GB)");

// ------------------------------------------------------------------ 4. particionamento
run("wpeutil UpdateBootInfo");
// Firmware: PEFirmwareType (1 = BIOS, 2 = UEFI). Se a leitura falhar, decide pelo
// bcdedit (o carregador do WinPE em uso termina em .efi no UEFI e .exe no BIOS). Se nem
// assim souber: MBR (se o disco tiver menos de 2 TB) com a partição de sistema em FAT32
// ativa e "bcdboot /f ALL", que inicia tanto em BIOS quanto em UEFI.
var fw = 0, fwHow = "";
try { fw = sh.RegRead("HKLM\\SYSTEM\\CurrentControlSet\\Control\\PEFirmwareType"); } catch (e) { fw = 0; }
var mode = fw === 2 ? "uefi" : fw === 1 ? "bios" : "";
if (mode) fwHow = "PEFirmwareType=" + fw;
else {
    var be = run("bcdedit /enum {current}").out;
    if (/\.efi\s*$/im.test(be)) mode = "uefi";
    else if (/\.exe\s*$/im.test(be)) mode = "bios";
    fwHow = "PEFirmwareType ilegível (" + fw + "); bcdedit: " + (mode || "sem resposta");
}
var both = !mode;  // desconhecido: serve aos dois
var uefi = mode === "uefi" || (both && best.size >= 2 * 1099511627776);  // >= 2 TB não cabe em MBR
log("firmware: " + (mode ? mode.toUpperCase() : "DESCONHECIDO -> " + (uefi ? "GPT" : "MBR") + " + bcdboot /f ALL") + " (" + fwHow + ")");

var used = lettersInUse(), free = [], pref = "WSTUVRQPONMLKJIHG";
for (var i = 0; i < pref.length; i++) if (!used[pref.charAt(i)]) free.push(pref.charAt(i));
if (free.length < 2) fail("Não há letras de unidade livres para particionar.", false);
var W = free[0], S = free[1];

var dp = ["select disk " + best.index, "clean"];
if (uefi) {
    dp.push("convert gpt", "create partition efi size=300", "format quick fs=fat32 label=\"System\"",
        "assign letter=" + S, "create partition msr size=16");
} else {
    // desconhecido: FAT32 na partição de sistema (o UEFI só lê FAT; o BIOS inicia de FAT32 ativa)
    dp.push("convert mbr", "create partition primary size=500", "format quick fs=" + (both ? "fat32" : "ntfs") + " label=\"System\"",
        "active", "assign letter=" + S);
}
dp.push("create partition primary", "format quick fs=ntfs label=\"Windows\"", "assign letter=" + W, "exit");
writeText(DIR + "\\diskpart.txt", dp.join("\r\n") + "\r\n", false);
var r = run("diskpart /s " + DIR + "\\diskpart.txt");
if (r.code !== 0 || !fso.FolderExists(W + ":\\")) fail("O diskpart falhou ao preparar o disco " + best.index + ".\r\n\r\n" + r.out, true);

// ------------------------------------------------------------------ 5. imagem + boot
var ap = "dism /Apply-Image /ImageFile:\"" + media.file + "\"" + (media.swm ? " /SWMFile:\"" + media.swm + "\"" : "") +
    " /Name:\"" + UAB.image + "\" /ApplyDir:" + W + ":\\";
r = run(ap);
if (r.code !== 0) {
    var info = run("dism /Get-WimInfo /WimFile:\"" + media.file + "\"");
    fail("O dism não conseguiu aplicar a edição \"" + UAB.image + "\" (código " + r.code + ").\r\n\r\nEdições nesta mídia:\r\n" + info.out, true);
}
var bcd = fso.FileExists(W + ":\\Windows\\System32\\bcdboot.exe") ? W + ":\\Windows\\System32\\bcdboot.exe" : "bcdboot.exe";
r = run(bcd + " " + W + ":\\Windows /s " + S + ": /f " + (both ? "ALL" : uefi ? "UEFI" : "BIOS"));
if (r.code !== 0) fail("O bcdboot falhou ao criar a inicialização (código " + r.code + ").\r\n\r\n" + r.out, true);

// ------------------------------------------------------------------ 6. respostas + marca
var pan = W + ":\\Windows\\Panther";
if (!fso.FolderExists(pan)) fso.CreateFolder(pan);
fso.CopyFile(answer, pan + "\\unattend.xml", true);
var ud = W + ":\\ProgramData\\uab";
if (!fso.FolderExists(ud)) fso.CreateFolder(ud);
if (!fso.FolderExists(ud + "\\logs")) fso.CreateFolder(ud + "\\logs");
writeText(ud + "\\build-id", UAB.build + "\r\n", false);
log("pronto: Windows aplicado no disco " + best.index + "; reiniciando");
try { fso.CopyFile(LOG, ud + "\\logs\\pe.log", true); } catch (e) { }
run("wpeutil reboot");
WScript.Sleep(60000);
