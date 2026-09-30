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

// persist grava um texto + o log em TODAS as unidades prontas, para sobreviver ao reboot
// (no modo sem pendrive não há técnico na tela, e o WinPE está na RAM: o log se perderia).
function persist(text) {
    var body = "=== ISO Builder (pe.js) ===\r\n" + text + "\r\n\r\n=== LOG ===\r\n";
    try { body += readText(LOG); } catch (e) { }
    var e2 = new Enumerator(fso.Drives);
    for (; !e2.atEnd(); e2.moveNext()) {
        try { if (e2.item().IsReady) writeText(e2.item().DriveLetter + ":\\uab-crash.txt", body, false); } catch (x) { }
    }
}

function fail(msg, erased) {
    log("FALHA: " + msg);
    if (typeof NOPEN !== "undefined" && NOPEN) {
        // sem pendrive: ninguém para fechar um Bloco de Notas; grava o motivo e reinicia
        persist("FALHA: " + msg + (erased ? " (o disco já havia sido alterado!)" : " (nada foi apagado)"));
        run("wpeutil reboot");
        WScript.Sleep(60000);
        WScript.Quit(1);
    }
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
try {
log("=== ISO Builder: instalação no WinPE (build " + UAB.build + ") ===");
var roots = readyRoots();
log("unidades: " + roots.join(" "));

// Marcadores do modo "sem pendrive" (gravador local): NOPEN preserva a partição da imagem
// e nunca abre janelas (não há técnico na tela); DRY só escreve o plano e não apaga nada.
function anyRootHas(name) {
    for (var i = 0; i < roots.length; i++) { if (fso.FileExists(roots[i] + ":\\" + name)) return true; }
    return fso.FileExists("X:\\" + name);
}
var NOPEN = anyRootHas("uab-nopendrive");
var DRY = anyRootHas("uab-dryrun");
if (NOPEN) log("modo SEM PENDRIVE" + (DRY ? " (TESTE: nao apaga nada)" : ""));

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
// checagem o PC seria apagado e reinstalado em laço. (Sem pendrive não há esse laço,
// e não há técnico na tela para responder, então pulamos.)
for (var i = 0; !NOPEN && i < roots.length; i++) {
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

function indexOfArr(a, v) { for (var i = 0; i < a.length; i++) if (a[i] === v) return i; return -1; }

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

// mediaDiskPart: disco e número da partição de uma unidade, pelo diskpart (a associação de
// letras por WMI NÃO é confiável no WinPE). Devolve {disk, part} (-1/0 se não achar).
function mediaDiskPart(letter) {
    writeText(DIR + "\\dp-detail.txt", "select volume " + letter + "\r\ndetail partition\r\ndetail volume\r\nexit\r\n", false);
    var out = run("diskpart /s " + DIR + "\\dp-detail.txt").out;
    var r = { disk: -1, part: 0 };
    var mp = out.match(/^\s*Parti\S*\s+(\d+)\s*$/im);   // "Partição 3" / "Partition 3"
    if (mp) r.part = parseInt(mp[1], 10);
    var md = out.match(/^\s*\*\s*Dis\S*\s+(\d+)/im);   // "* Disco 0" / "* Disk 0"
    if (md) r.disk = parseInt(md[1], 10);
    return r;
}

// diskInfo: modelo/tamanho/USB de um disco pelo índice (a enumeração de discos por WMI
// funciona no WinPE; só a associação de letras que não).
function diskInfo(idx) {
    var e = new Enumerator(wmi.ExecQuery("SELECT * FROM Win32_DiskDrive WHERE Index=" + idx));
    if (e.atEnd()) return null;
    var d = e.item(), itf = String(d.InterfaceType || ""), pnp = String(d.PNPDeviceID || ""), mt = String(d.MediaType || "");
    var usb = /^USB/i.test(itf) || /USBSTOR|UASPSTOR/i.test(pnp) || /1394/i.test(itf) || /removable|remov|external|extern/i.test(mt);
    var sz = parseFloat(d.Size || "0");
    return { index: idx, size: sz, gb: Math.floor(sz / 1e9), model: String(d.Model || ""), usb: usb };
}

var best = null, seen = [], PRESERVE = false, keepPart = 0;

if (NOPEN) {
    // Sem pendrive: a imagem está numa partição do PRÓPRIO disco. Acha disco+partição por
    // diskpart, confirma que é disco interno, e entra no modo PRESERVAÇÃO (apaga o resto e
    // MANTÉM a partição da imagem). Assim nunca apaga a imagem por engano.
    var mdp = mediaDiskPart(media.letter);
    if (mdp.disk < 0 || !mdp.part) fail("Não consegui identificar o disco/partição da imagem (" + media.letter + ":).", false);
    var di = diskInfo(mdp.disk);
    if (!di) fail("Não consegui ler o disco " + mdp.disk + ".", false);
    if (di.usb) fail("A imagem está num disco USB; a instalação sem pendrive só apaga o disco interno.", false);
    if (di.size < UAB.minGB * 1e9) fail("O disco é pequeno demais (mínimo " + UAB.minGB + " GB).", false);
    PRESERVE = true; keepPart = mdp.part; best = di;
    log("PRESERVAÇÃO: disco " + best.index + " (" + best.model + "), preservando a partição " + keepPart + " (" + media.letter + ":)");
} else {
    // Com pendrive: maior disco interno (exclui USB/removível e o que contém a mídia/respostas).
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
}

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

var dp = ["select disk " + best.index];
if (PRESERVE) {
    // apaga as partições ANTES da reservada (a reservada é a última: foi criada encolhendo o C:).
    // apagar "partition 1" (keepPart-1) vezes remove as da frente; a reservada vira a nº 1.
    for (var k = 0; k < keepPart - 1; k++) dp.push("select partition 1", "delete partition override");
    if (uefi) dp.push("create partition efi size=300", "format quick fs=fat32 label=\"System\"", "assign letter=" + S, "create partition msr size=16");
    else dp.push("create partition primary size=500", "format quick fs=" + (both ? "fat32" : "ntfs") + " label=\"System\"", "active", "assign letter=" + S);
} else {
    dp.push("clean");
    if (uefi) dp.push("convert gpt", "create partition efi size=300", "format quick fs=fat32 label=\"System\"", "assign letter=" + S, "create partition msr size=16");
    else dp.push("convert mbr", "create partition primary size=500", "format quick fs=" + (both ? "fat32" : "ntfs") + " label=\"System\"", "active", "assign letter=" + S);
}
dp.push("create partition primary", "format quick fs=ntfs label=\"Windows\"", "assign letter=" + W, "exit");

// MODO TESTE (sem pendrive): não apaga nada. Escreve o plano e o disco real na partição
// reservada e reinicia, para o técnico conferir antes de ligar o apagamento de verdade.
if (DRY) {
    var probe = ["select disk " + best.index, "detail disk", "list partition", "exit"].join("\r\n") + "\r\n";
    writeText(DIR + "\\probe.txt", probe, false);
    var layout = run("diskpart /s " + DIR + "\\probe.txt").out;
    var plan = "=== ISO Builder: PLANO (modo teste, NADA foi apagado) ===\r\n" +
        "disco alvo: " + best.index + " (" + best.model + ", " + best.gb + " GB)\r\n" +
        "partição reservada preservada: " + keepPart + " (" + media.letter + ":)\r\n" +
        "firmware: " + (mode || "desconhecido") + "\r\n\r\n=== diskpart que SERIA executado ===\r\n" +
        dp.join("\r\n") + "\r\n\r\n=== disco real agora ===\r\n" + layout + "\r\n";
    writeText(media.letter + ":\\uab-plan.txt", plan, false);
    try { fso.CopyFile(LOG, media.letter + ":\\uab-pe.log", true); } catch (e) { }
    persist("MODO TESTE concluído — plano:\r\n\r\n" + plan); // grava em todas as unidades, por garantia
    log("MODO TESTE: plano escrito em " + media.letter + ":\\uab-plan.txt; reiniciando sem apagar");
    run("wpeutil reboot");
    WScript.Sleep(60000);
    WScript.Quit(0);
}

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

// BIOS: garante o código de boot no MBR do disco e no VBR da partição de sistema. O bcdboot
// grava os ARQUIVOS (bootmgr + BCD), mas ao reaproveitar um disco sem "clean" (modo
// PRESERVAÇÃO) nem sempre reescreve o bootstrap do MBR/VBR — sem ele o BIOS mostra "Reboot
// and select proper boot device". O bootsect é idempotente e no UEFI o MBR é irrelevante.
if (!uefi) {
    var bsExe = fso.FileExists(W + ":\\Windows\\System32\\bootsect.exe") ? W + ":\\Windows\\System32\\bootsect.exe" : "bootsect.exe";
    var rbs = run(bsExe + " /nt60 " + S + ": /mbr /force");
    if (rbs.code !== 0) log("aviso: bootsect /mbr retornou " + rbs.code + " (seguindo assim mesmo): " + rbs.out);
}

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
// Sem pendrive: a partição da imagem (UABMEDIA) é PRESERVADA e sobrevive ao reboot. Se o PC
// não ligar, dá para lê-la de um live USB e ver que o pe.js concluiu (log completo aqui).
if (NOPEN) {
    try { writeText(media.letter + ":\\uab-done.txt", "pe.js CONCLUIU a instalacao no disco " + best.index +
        " (" + best.model + ") em " + new Date().toString() + "\r\nfirmware: " + (mode || "desconhecido") +
        (uefi ? " (GPT/UEFI)" : " (MBR/BIOS)") + "\r\n", false); } catch (e) { }
    try { fso.CopyFile(LOG, media.letter + ":\\uab-lastrun.log", true); } catch (e) { }
}
run("wpeutil reboot");
WScript.Sleep(60000);
} catch (uabEx) {
    // qualquer erro inesperado: no modo sem pendrive grava o motivo e reinicia (não trava)
    if (typeof NOPEN !== "undefined" && NOPEN) {
        try { persist("EXCEÇÃO: " + (uabEx.message || uabEx) + " (num " + (uabEx.number || "?") + ")"); } catch (e) { }
        try { run("wpeutil reboot"); } catch (e) { }
        WScript.Sleep(60000);
    } else {
        try { tell("Erro inesperado: " + (uabEx.message || uabEx)); } catch (e) { }
    }
    WScript.Quit(1);
}
