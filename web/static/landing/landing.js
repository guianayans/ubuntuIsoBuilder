// Página inicial pública do isoBuilder: animação guiada pela rolagem (240 quadros
// renderizados em 3D) e a cena das 4 etapas. Script externo por causa da CSP do painel.
(() => {
  const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
  const seq = document.getElementById("seq"), cv = document.getElementById("frames"), ctx = cv.getContext("2d");
  const caps = [...document.querySelectorAll(".cap")];
  // 240 quadros renderizados em 3D (three.js, materiais físicos). A rolagem define o
  // quadro-alvo; o quadro exibido desliza até ele com inércia e mistura os dois quadros
  // vizinhos, para o movimento não andar em degraus.
  const N = 240, bmp = new Array(N);
  let target = 0, shown = 0, lastKey = "", anim = 0, lastT = 0;
  const src = i => "/static/landing/frames/" + String(i).padStart(3, "0") + ".webp";
  function nearest(i) {        // o quadro pedido ou o carregado mais próximo (enquanto os outros chegam)
    for (let d = 0; d < N; d++) { if (bmp[i - d]) return i - d; if (bmp[i + d]) return i + d; }
    return -1;
  }
  function paint(pos) {
    const k = Math.floor(pos), f = pos - k;
    const a = nearest(k), b = nearest(Math.min(N - 1, k + 1));
    if (a < 0) return;
    const key = a + ":" + b + ":" + Math.round(f * 48);
    if (key === lastKey) return;
    lastKey = key;
    ctx.globalAlpha = 1; ctx.drawImage(bmp[a], 0, 0, cv.width, cv.height);
    if (b !== a && f > 0.02) { ctx.globalAlpha = f; ctx.drawImage(bmp[b], 0, 0, cv.width, cv.height); ctx.globalAlpha = 1; }
  }
  function tick(t) {
    const dt = Math.min(64, t - (lastT || t)); lastT = t;
    shown += (target - shown) * (1 - Math.exp(-dt / 85));   // inércia (~85 ms), igual em 60 ou 120 Hz
    if (Math.abs(target - shown) < 0.01) shown = target;
    paint(shown);
    if (shown !== target) anim = requestAnimationFrame(tick); else { anim = 0; lastT = 0; }
  }
  function goTo(pos) { target = pos; if (!anim) anim = requestAnimationFrame(tick); }
  function load(i) {
    if (bmp[i] !== undefined) return;
    bmp[i] = null;
    const im = new Image(); im.src = src(i);
    // decodifica antes de usar (sem engasgo ao desenhar); a imagem fica com o navegador,
    // que libera memória se precisar (240 quadros decodificados passariam de 900 MB)
    im.decode()
      .then(() => { bmp[i] = im; if (!anim) { lastKey = ""; paint(shown); } })
      .catch(() => { bmp[i] = undefined; });
  }
  load(0); load(N - 1);
  [24, 12, 6, 3, 1].forEach(step => { for (let i = 0; i < N; i += step) load(i); });
  const story = document.getElementById("como"), beats = [...document.querySelectorAll(".beat")];
  const scenes = [...document.querySelectorAll(".scene")], rail = [...document.querySelectorAll("#rail i")];
  const m1 = document.getElementById("m1"), m1t = document.getElementById("m1t"), sha = document.getElementById("sha");
  const m2 = document.getElementById("m2"), m2t = document.getElementById("m2t"), newpc = document.getElementById("newpc");
  const titles = ["iso.gvtserver.online / nova-iso", "isoBuilder · download", "isoBuilder · gravação", "iso.gvtserver.online / maquinas"];
  const title = document.getElementById("scrtitle");
  let cur = 0, ticking = false;
  const clamp = v => Math.max(0, Math.min(1, v));

  function frame() {
    ticking = false;
    const vh = innerHeight;
    // sequência: 0 no topo da seção, 1 no fim da parte presa
    const qr = seq.getBoundingClientRect();
    const sp = clamp(-qr.top / (qr.height - vh));
    seq.style.setProperty("--sp", sp.toFixed(3));
    goTo(sp * (N - 1));
    caps.forEach(c => c.classList.toggle("on", sp >= +c.dataset.a && sp < +c.dataset.b));
    // cena presa: progresso 0..1 enquanto o bloco alto passa
    const sr = story.getBoundingClientRect();
    const p = clamp(-sr.top / (sr.height - vh));
    const step = Math.min(3, Math.floor(p * 4));
    const local = clamp(p * 4 - step);
    rail.forEach((r, i) => r.style.setProperty("--f", i < step ? 1 : i === step ? local.toFixed(3) : 0));
    if (step !== cur) {
      beats[cur].classList.remove("on"); scenes[cur].classList.remove("on");
      beats[step].classList.add("on"); scenes[step].classList.add("on");
      title.textContent = titles[step];
      if (step === 3) { newpc.classList.remove("new"); void newpc.offsetWidth; newpc.classList.add("new"); }
      cur = step;
    }
    // barras de progresso acompanham a rolagem dentro da etapa
    const a = step > 1 ? 1 : step === 1 ? local : 0;
    m1.parentNode.style.setProperty("--m", a); m1.style.width = (a * 100) + "%";
    m1t.textContent = Math.round(a * 100) + "%";
    sha.textContent = a >= .98 ? "✔ confere" : "conferindo…";
    const b = step > 2 ? 1 : step === 2 ? local : 0;
    m2.style.width = (b * 100) + "%"; m2t.textContent = Math.round(b * 100) + "%";
  }
  // Posição inicial: o pendrive começa logo abaixo dos botões do título, em qualquer
  // tamanho de janela. No 1º quadro ele ocupa de 34% a 62% da altura da imagem.
  const stick = seq.querySelector(".stick"), head = seq.querySelector(".head");
  function place() {
    const sh = stick.clientHeight, ch = cv.offsetHeight, cw = cv.offsetWidth;
    const content = Math.min(ch, cw * 10 / 16);               // área real desenhada (object-fit: contain)
    const top = sh / 2 - 0.56 * ch + (ch - content) / 2;       // topo da imagem sem deslocamento
    const objTop = top + 0.338 * content, objBot = top + 0.625 * content;
    const headBot = head.offsetTop + head.offsetHeight;
    let off = Math.max(0, headBot + 28 - objTop);
    off = Math.min(off, Math.max(0, sh - 12 - objBot));       // sem passar do fim da tela
    seq.style.setProperty("--start", off + "px");
  }
  place(); addEventListener("resize", place);
  if (document.fonts) document.fonts.ready.then(place);       // o título muda de altura quando a fonte carrega
  function onScroll() { if (!ticking) { ticking = true; requestAnimationFrame(frame); } }
  if (!reduce) {
    addEventListener("scroll", onScroll, { passive: true });
    addEventListener("resize", onScroll);
    frame();
  } else {
    target = shown = N - 1; lastKey = ""; paint(shown);
    caps.forEach(c => c.classList.add("on"));
    beats.forEach(b => b.classList.add("on")); scenes.forEach(s => s.classList.add("on"));
    m1.style.width = m2.style.width = "100%"; m1t.textContent = m2t.textContent = "100%"; sha.textContent = "✔ confere";
  }
})();
