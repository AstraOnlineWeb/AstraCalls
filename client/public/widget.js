/*
 * AstraCalls — widget de chamada para o Chatwoot.
 * Carregue no Chatwoot (super_admin > app_config > internal), no campo de scripts:
 *   <script src="https://SEU-WACALLS/widget.js" data-api-key="SUA_WIDGET_KEY"></script>
 * IMPORTANTE (segurança, issue #10): use aqui a CHAVE DE WIDGET
 * (WACALLS_WIDGET_KEY), NÃO a chave-mestra (WACALLS_API_KEY). A chave fica
 * visível no DOM/rede pra todo agente; a de widget só autoriza resolver contato,
 * receber eventos e operar chamadas — nunca listar/apagar sessões ou mandar
 * mensagens. Se WACALLS_WIDGET_KEY não estiver configurada, a mestra ainda
 * funciona (compatível), mas aí a chave exposta tem acesso total — evite.
 * Injeta um ícone de telefone ao lado do botão de excluir ticket; ao clicar,
 * abre um painel flutuante e liga para o contato via WhatsApp (WebRTC).
 * Numa conversa de GRUPO do WhatsApp, liga PARA O GRUPO (chamada em grupo, áudio
 * ou vídeo — requer WACALLS_GROUP_CALLS=1 no servidor).
 */
(function () {
  "use strict";
  var script = document.currentScript;
  var BASE = (script && script.getAttribute("data-url")) || (script ? new URL(script.src).origin : "");
  var KEY = (script && script.getAttribute("data-api-key")) || "";
  var ANCHOR = (script && script.getAttribute("data-anchor")) || "";

  var PHONE_SVG =
    '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72c.13.96.36 1.9.7 2.81a2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45c.91.34 1.85.57 2.81.7A2 2 0 0 1 22 16.92z"/></svg>';
  var ICON_PHONE =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 16.92v3a2 2 0 0 1-2.18 2 19.79 19.79 0 0 1-8.63-3.07 19.5 19.5 0 0 1-6-6 19.79 19.79 0 0 1-3.07-8.67A2 2 0 0 1 4.11 2h3a2 2 0 0 1 2 1.72c.13.96.36 1.9.7 2.81a2 2 0 0 1-.45 2.11L8.09 9.91a16 16 0 0 0 6 6l1.27-1.27a2 2 0 0 1 2.11-.45c.91.34 1.85.57 2.81.7A2 2 0 0 1 22 16.92z"/></svg>';
  var ICON_PHONE_OFF =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.68 13.31a16 16 0 0 0 3.41 2.6l1.27-1.27a2 2 0 0 1 2.11-.45 12.84 12.84 0 0 0 2.81.7 2 2 0 0 1 1.72 2v3a2 2 0 0 1-2.18 2A19.79 19.79 0 0 1 3.07 8.94a2 2 0 0 1 2-2.18h3"/><line x1="23" y1="1" x2="1" y2="23"/></svg>';
  var ICON_MIC =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><line x1="12" y1="19" x2="12" y2="23"/></svg>';
  var ICON_MIC_OFF =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="1" y1="1" x2="23" y2="23"/><path d="M9 9v3a3 3 0 0 0 5.12 2.12M15 9.34V4a3 3 0 0 0-5.94-.6"/><path d="M17 16.95A7 7 0 0 1 5 12v-2m14 0v2a7 7 0 0 1-.11 1.23"/><line x1="12" y1="19" x2="12" y2="23"/></svg>';
  var ICON_WARN =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>';
  var ICON_VIDEO =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="23 7 16 12 23 17 23 7"/><rect x="1" y="5" width="15" height="14" rx="2" ry="2"/></svg>';
  var ICON_VIDEO_OFF =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 16v1a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h2m5.66 0H14a2 2 0 0 1 2 2v3.34l1 1L23 7v10"/><line x1="1" y1="1" x2="23" y2="23"/></svg>';

  var ICON_USERS =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/></svg>';
  var ICON_ROTATE =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/></svg>';

  // Parâmetros do vídeo (espelham client/src/constants/video.ts). O transporte é um
  // datachannel "h264" out-of-order com WebCodecs; o backend já trata isso.
  var VIDEO = { LABEL: "h264", CODEC: "avc1.42E01F", W: 160, H: 120, FPS: 15, BITRATE: 50000, KF: 15 };

  function api(path, opts) {
    opts = opts || {};
    return fetch(BASE + path, {
      method: opts.method || "GET",
      headers: { "Content-Type": "application/json", "X-API-Key": KEY },
      body: opts.body ? JSON.stringify(opts.body) : undefined,
    }).then(function (r) {
      if (!r.ok) throw new Error(path + " " + r.status);
      return r.json();
    });
  }

  // ---------- transporte da chamada (WebRTC vs WebSocket) ----------
  // Config do servidor: defaultTransport ("websocket" força WS onde o WebRTC/UDP
  // não fecha, ex.: atrás de proxy). Carregada uma vez e cacheada.
  var CFG = { defaultTransport: "" };
  var cfgPromise = null;
  function ensureConfig() {
    if (!cfgPromise) {
      cfgPromise = api("/api/config")
        .then(function (c) { CFG.defaultTransport = (c && c.defaultTransport) || ""; })
        .catch(function () {});
    }
    return cfgPromise;
  }
  function pickTransport() {
    return CFG.defaultTransport === "websocket" ? "websocket" : "webrtc";
  }

  // Transporte de áudio por WebSocket (PCM16 16kHz) — espelha
  // client/src/lib/ws-audio.ts em JS puro. Áudio-only (sem vídeo). Passa em
  // WSS/443, portanto funciona atrás de proxy/firewall que bloqueia UDP.
  var WS_SR = 16000, WS_FRAME = 512, WS_CUSHION = 0.06;
  function f32toI16(f) {
    var o = new Int16Array(f.length);
    for (var i = 0; i < f.length; i++) { var s = Math.max(-1, Math.min(1, f[i])); o[i] = s < 0 ? s * 32768 : s * 32767; }
    return o;
  }
  function wsUrl(path) {
    var wsBase = BASE.replace(/^https:\/\//, "wss://").replace(/^http:\/\//, "ws://");
    return wsBase + path + (KEY ? (path.indexOf("?") >= 0 ? "&" : "?") + "apiKey=" + encodeURIComponent(KEY) : "");
  }
  // path: "/calls/{id}/ws" (1:1) ou "/calls/group/ws" (grupo). onClose: chamado se o
  // servidor fechar o WS (chamada encerrada do outro lado).
  async function openWSAudio(session, path, onClose) {
    var mic = await navigator.mediaDevices.getUserMedia({
      audio: { sampleRate: WS_SR, channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
    var url = wsUrl("/api/sessions/" + session + path);
    var ws = new WebSocket(url, ["pcm16"]);
    ws.binaryType = "arraybuffer";
    var capCtx = null, playCtx = null, srcNode = null, proc = null, playCursor = 0, closed = false;
    function startCapture() {
      capCtx = new AudioContext({ sampleRate: WS_SR });
      srcNode = capCtx.createMediaStreamSource(mic);
      proc = capCtx.createScriptProcessor(WS_FRAME, 1, 1);
      proc.onaudioprocess = function (ev) {
        if (ws.readyState !== WebSocket.OPEN) return;
        ws.send(f32toI16(ev.inputBuffer.getChannelData(0)).buffer);
      };
      srcNode.connect(proc); proc.connect(capCtx.destination);
    }
    function playPCM(buf) {
      if (!playCtx) playCtx = new AudioContext({ sampleRate: WS_SR });
      var i16 = new Int16Array(buf); if (!i16.length) return;
      var f = new Float32Array(i16.length);
      for (var i = 0; i < i16.length; i++) f[i] = i16[i] / 32768;
      var ab = playCtx.createBuffer(1, f.length, WS_SR); ab.copyToChannel(f, 0);
      var s = playCtx.createBufferSource(); s.buffer = ab; s.connect(playCtx.destination);
      var now = playCtx.currentTime;
      if (playCursor < now + 0.005) playCursor = now + WS_CUSHION;
      s.start(playCursor); playCursor += ab.duration;
    }
    await new Promise(function (resolve, reject) {
      ws.onopen = function () { resolve(); };
      ws.onerror = function () { reject(new Error("ws audio failed")); };
      ws.onmessage = function (ev) { if (ev.data instanceof ArrayBuffer) playPCM(ev.data); };
    });
    ws.onclose = function () { if (!closed && onClose) onClose(); };
    startCapture();
    return {
      mic: mic,
      setMic: function (on) { mic.getAudioTracks().forEach(function (t) { t.enabled = on; }); },
      close: function () {
        if (closed) return; closed = true;
        try { proc && proc.disconnect(); } catch (e) {}
        try { srcNode && srcNode.disconnect(); } catch (e) {}
        try { capCtx && capCtx.close(); } catch (e) {}
        try { playCtx && playCtx.close(); } catch (e) {}
        try { mic.getTracks().forEach(function (t) { t.stop(); }); } catch (e) {}
        try { if (ws.readyState === WebSocket.OPEN) ws.close(1000, "call ended"); } catch (e) {}
      },
    };
  }

  // ---------- estilos ----------
  var style = document.createElement("style");
  style.textContent =
    "#wacalls-btn{display:inline-flex;align-items:center;justify-content:center;cursor:pointer;color:#687076}" +
    "#wacalls-btn:hover{color:#11181c}" +
    "#wacalls-panel{position:fixed;bottom:20px;right:20px;width:296px;background:#fff;color:#11181c;border:1px solid #dfe3e6;border-radius:12px;box-shadow:0 12px 32px rgba(17,24,28,.12);z-index:99999;font-family:'Inter','InterDisplay',-apple-system,system-ui,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;overflow:hidden;-webkit-font-smoothing:antialiased}" +
    "#wacalls-panel .cw-h{display:flex;align-items:center;gap:8px;padding:12px 14px;border-bottom:1px solid #eceef0;font-size:14px;font-weight:600;color:#11181c;letter-spacing:-.01em}" +
    "#wacalls-panel .cw-dot{width:7px;height:7px;border-radius:50%;background:#2781F6;flex:none}" +
    "#wacalls-panel .cw-h-t{flex:1}" +
    "#wacalls-panel .cw-x{cursor:pointer;background:none;border:0;color:#889096;font-size:20px;line-height:1;padding:0;width:24px;height:24px;border-radius:6px;display:inline-flex;align-items:center;justify-content:center;transition:background .12s,color .12s}" +
    "#wacalls-panel .cw-x:hover{background:#f1f3f5;color:#11181c}" +
    "#wacalls-panel .cw-b{padding:22px 16px 24px;text-align:center}" +
    "#wacalls-panel .cw-name{font-weight:600;font-size:15px;color:#11181c;letter-spacing:-.01em}" +
    "#wacalls-panel .cw-sub{font-size:13px;color:#687076;margin-top:3px;font-variant-numeric:tabular-nums}" +
    "#wacalls-panel .cw-st{font-size:12px;font-weight:500;color:#687076;margin-top:14px;min-height:16px;font-variant-numeric:tabular-nums;letter-spacing:.01em}" +
    "#wacalls-panel .cw-act{margin-top:18px;border:0;border-radius:50%;width:54px;height:54px;cursor:pointer;color:#fff;display:inline-flex;align-items:center;justify-content:center;transition:filter .15s,transform .08s}" +
    "#wacalls-panel .cw-act:hover{filter:brightness(.95)}" +
    "#wacalls-panel .cw-act:active{transform:scale(.95)}" +
    "#wacalls-panel .cw-act svg{width:22px;height:22px}" +
    "#wacalls-panel .cw-call{background:#30a46c;box-shadow:0 4px 12px rgba(48,164,108,.32)}" +
    "#wacalls-panel .cw-hang{background:#e5484d;box-shadow:0 4px 12px rgba(229,72,77,.3)}" +
    "#wacalls-panel .cw-row{display:flex;gap:16px;justify-content:center;align-items:center}" +
    "#wacalls-panel .cw-mute{background:#f1f3f5;color:#687076;width:46px;height:46px;box-shadow:none}" +
    "#wacalls-panel .cw-mute:hover{background:#e6e8eb;filter:none}" +
    "#wacalls-panel .cw-mute.on{background:#e5484d;color:#fff}" +
    "#wacalls-panel .cw-mute svg{width:19px;height:19px}" +
    "#wacalls-panel .cw-warn{width:44px;height:44px;margin:0 auto 12px;border-radius:50%;background:#fff7c2;color:#9e6c00;display:flex;align-items:center;justify-content:center}" +
    "#wacalls-panel .cw-warn svg{width:24px;height:24px}" +
    "#wacalls-panel .cw-video{position:relative;margin:14px 0 2px;border-radius:10px;overflow:hidden;background:#000;aspect-ratio:4/3}" +
    "#wacalls-panel .cw-video .cw-remote-v{width:100%;height:100%;object-fit:cover;display:block;background:#000}" +
    "#wacalls-panel .cw-video .cw-local-v{position:absolute;right:8px;bottom:8px;width:72px;border-radius:6px;border:1px solid rgba(255,255,255,.25);object-fit:cover;background:#111}" +
    "#wacalls-panel .cw-cam.on{background:#2781F6;color:#fff}" +
    "#wacalls-panel .cw-rot{position:absolute;top:6px;right:6px;width:26px;height:26px;border:0;border-radius:6px;background:rgba(0,0,0,.55);color:#fff;cursor:pointer;display:inline-flex;align-items:center;justify-content:center;padding:0}" +
    "#wacalls-panel .cw-rot svg{width:14px;height:14px}" +
    "#wacalls-panel .cw-video video{transition:transform .2s}" +
    "#wacalls-panel.cw-wide{width:460px}" +
    "#wacalls-panel .cw-tiles{display:grid;grid-template-columns:repeat(2,1fr);gap:6px;margin:14px 0 2px}" +
    "#wacalls-panel .cw-tile{position:relative;border-radius:8px;overflow:hidden;background:#000;aspect-ratio:4/3}" +
    "#wacalls-panel .cw-tile video{width:100%;height:100%;object-fit:cover;display:block;background:#000;transition:transform .2s}" +
    "#wacalls-panel .cw-lbl{position:absolute;left:6px;bottom:6px;max-width:calc(100% - 12px);overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:10px;color:#fff;background:rgba(0,0,0,.55);border-radius:4px;padding:1px 5px}" +
    "#wacalls-panel .cw-row2{display:flex;gap:12px;justify-content:center;margin-top:18px}" +
    "#wacalls-panel .cw-grp{background:#2781F6;box-shadow:0 4px 12px rgba(39,129,246,.3)}";
  document.head.appendChild(style);

  // ---------- estado ----------
  var call = null; // {pc, mic, callId, session, t0, timer}
  var incoming = null; // chamada recebida pendente {sessionId, callId, peer}
  var globalES = null; // SSE persistente p/ detectar chamadas recebidas
  var ringCtx = null, ringTimer = null;

  function playRing() {
    stopRing();
    try {
      var AC = window.AudioContext || window.webkitAudioContext;
      if (!AC) return;
      ringCtx = new AC();
      var beep = function () {
        if (!ringCtx) return;
        var o = ringCtx.createOscillator(), g = ringCtx.createGain();
        o.type = "sine";
        o.frequency.value = 480;
        o.connect(g);
        g.connect(ringCtx.destination);
        var t = ringCtx.currentTime;
        g.gain.setValueAtTime(0.0001, t);
        g.gain.exponentialRampToValueAtTime(0.18, t + 0.05);
        g.gain.exponentialRampToValueAtTime(0.0001, t + 0.9);
        o.start(t);
        o.stop(t + 0.95);
      };
      beep();
      ringTimer = setInterval(beep, 2500);
    } catch (e) {}
  }
  function stopRing() {
    if (ringTimer) { clearInterval(ringTimer); ringTimer = null; }
    if (ringCtx) { try { ringCtx.close(); } catch (e) {} ringCtx = null; }
  }

  function el(tag, cls, html) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (html != null) e.innerHTML = html;
    return e;
  }

  function closePanel() {
    if (call) hangup();
    else if (incoming) rejectIncoming();
    stopRing();
    var p = document.getElementById("wacalls-panel");
    if (p) p.remove();
  }

  function panel() {
    var p = document.getElementById("wacalls-panel");
    if (p) return p;
    p = el("div");
    p.id = "wacalls-panel";
    document.body.appendChild(p);
    return p;
  }

  function render(state) {
    var p = panel();
    var head =
      '<div class="cw-h"><span class="cw-dot"></span><span class="cw-h-t">Chamada</span><span class="cw-x" id="wacalls-close">&times;</span></div>';
    var body = "";
    if (state.loading) {
      body = '<div class="cw-b"><div class="cw-sub">Identificando contato…</div></div>';
    } else if (state.warn) {
      body =
        '<div class="cw-b"><div class="cw-warn">' + ICON_WARN + "</div>" +
        '<div class="cw-name">Caixa sem WhatsApp</div>' +
        '<div class="cw-sub" style="margin-top:6px;line-height:1.45">' + esc(state.warn) + "</div></div>";
    } else if (state.error) {
      body = '<div class="cw-b"><div class="cw-sub" style="color:#e5484d">' + state.error + "</div></div>";
    } else if (state.inGroupCall) {
      head = '<div class="cw-h"><span class="cw-dot"></span><span class="cw-h-t">Chamada em grupo</span><span class="cw-x" id="wacalls-close">&times;</span></div>';
      body =
        '<div class="cw-b"><div class="cw-name">' + esc(state.name) + "</div>" +
        '<div class="cw-sub">Grupo do WhatsApp</div>' +
        '<div class="cw-tiles" id="wacalls-tiles" style="display:none"></div>' +
        '<div class="cw-st" id="wacalls-st">' + (state.status || "") + "</div>" +
        '<div class="cw-row"><button class="cw-act cw-mute" id="wacalls-mute" title="Mudo">' + ICON_MIC + "</button>" +
        (state.video ? '<button class="cw-act cw-mute cw-cam on" id="wacalls-cam" title="Desligar vídeo">' + ICON_VIDEO + "</button>" : "") +
        '<button class="cw-act cw-hang" id="wacalls-hang" title="Encerrar">' + ICON_PHONE_OFF + "</button></div></div>";
    } else if (state.group) {
      head = '<div class="cw-h"><span class="cw-dot"></span><span class="cw-h-t">Chamada em grupo</span><span class="cw-x" id="wacalls-close">&times;</span></div>';
      body =
        '<div class="cw-b"><div class="cw-name">' + esc(state.name) + "</div>" +
        '<div class="cw-sub">Grupo do WhatsApp · ligar para todos os membros</div>' +
        (state.groupCalls
          ? '<div class="cw-row2"><button class="cw-act cw-call" id="wacalls-start-group" title="Ligar para o grupo (áudio)">' + ICON_PHONE + "</button>" +
            '<button class="cw-act cw-grp" id="wacalls-start-group-video" title="Ligar para o grupo com vídeo">' + ICON_VIDEO + "</button></div>"
          : '<div class="cw-st">Chamada em grupo desligada nesta instância (WACALLS_GROUP_CALLS).</div>') +
        "</div>";
    } else if (state.inCall) {
      body =
        '<div class="cw-b"><div class="cw-name">' + esc(state.name) + "</div>" +
        '<div class="cw-sub">' + esc(state.phone) + "</div>" +
        '<div class="cw-video" id="wacalls-video" style="display:none">' +
        '<video class="cw-remote-v" id="wacalls-remote-v" autoplay playsinline></video>' +
        '<video class="cw-local-v" id="wacalls-local-v" autoplay playsinline muted style="display:none"></video>' +
        '<button type="button" class="cw-rot" id="wacalls-rot" title="Girar vídeo">' + ICON_ROTATE + "</button></div>" +
        '<div class="cw-st" id="wacalls-st">' + (state.status || "") + "</div>" +
        '<div class="cw-row"><button class="cw-act cw-mute" id="wacalls-mute" title="Mudo">' + ICON_MIC + "</button>" +
        '<button class="cw-act cw-mute cw-cam" id="wacalls-cam" title="Ativar vídeo">' + ICON_VIDEO_OFF + "</button>" +
        '<button class="cw-act cw-hang" id="wacalls-hang" title="Encerrar">' + ICON_PHONE_OFF + "</button></div>" +
        '<audio id="wacalls-audio" autoplay></audio></div>';
    } else if (state.incoming) {
      body =
        '<div class="cw-b"><div class="cw-name">Chamada recebida</div>' +
        '<div class="cw-sub">' + esc(state.phone) + "</div>" +
        '<div class="cw-st" id="wacalls-st">Tocando…</div>' +
        '<div class="cw-row"><button class="cw-act cw-call" id="wacalls-answer" title="Atender">' + ICON_PHONE + "</button>" +
        '<button class="cw-act cw-hang" id="wacalls-reject" title="Recusar">' + ICON_PHONE_OFF + "</button></div>" +
        '<audio id="wacalls-audio" autoplay></audio></div>';
    } else {
      body =
        '<div class="cw-b"><div class="cw-name">' + esc(state.name) + "</div>" +
        '<div class="cw-sub">' + esc(state.phone) + "</div>" +
        '<button class="cw-act cw-call" id="wacalls-start" title="Ligar">' + ICON_PHONE + "</button></div>";
    }
    p.innerHTML = head + body;
    p.classList.toggle("cw-wide", !!(state.inGroupCall && state.video));
    p.querySelector("#wacalls-close").onclick = closePanel;
    if (p.querySelector("#wacalls-start-group"))
      p.querySelector("#wacalls-start-group").onclick = function () { startGroupCall(state, false); };
    if (p.querySelector("#wacalls-start-group-video"))
      p.querySelector("#wacalls-start-group-video").onclick = function () { startGroupCall(state, true); };
    if (p.querySelector("#wacalls-rot"))
      p.querySelector("#wacalls-rot").onclick = function () {
        if (!call) return;
        call.manualRot = ((call.manualRot || 0) + 90) % 360;
        updateVideoUI();
      };
    if (p.querySelector("#wacalls-start"))
      p.querySelector("#wacalls-start").onclick = function () {
        startCall(state);
      };
    if (p.querySelector("#wacalls-answer")) p.querySelector("#wacalls-answer").onclick = acceptIncoming;
    if (p.querySelector("#wacalls-reject")) p.querySelector("#wacalls-reject").onclick = rejectIncoming;
    if (p.querySelector("#wacalls-hang")) p.querySelector("#wacalls-hang").onclick = hangup;
    if (p.querySelector("#wacalls-mute"))
      p.querySelector("#wacalls-mute").onclick = function () {
        toggleMute(this);
      };
    if (p.querySelector("#wacalls-cam"))
      p.querySelector("#wacalls-cam").onclick = function () {
        toggleCam(this);
      };
  }

  function esc(s) {
    return (s || "").replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  function setStatus(t) {
    var s = document.getElementById("wacalls-st");
    if (s) s.textContent = t;
  }

  function iceComplete(pc) {
    return new Promise(function (res) {
      if (pc.iceGatheringState === "complete") return res();
      pc.addEventListener("icegatheringstatechange", function () {
        if (pc.iceGatheringState === "complete") res();
      });
    });
  }

  // ---------- vídeo (WebCodecs H264 sobre datachannel) ----------
  function videoSupported() {
    return (
      typeof window !== "undefined" &&
      "VideoEncoder" in window &&
      "VideoDecoder" in window &&
      "MediaStreamTrackProcessor" in window &&
      "MediaStreamTrackGenerator" in window
    );
  }

  function isAnnexBKeyframe(b) {
    for (var i = 0; i + 4 < b.length; i++) {
      if (b[i] !== 0 || b[i + 1] !== 0) continue;
      var sc = 0;
      if (b[i + 2] === 1) sc = 3;
      else if (b[i + 2] === 0 && b[i + 3] === 1) sc = 4;
      if (sc === 0) continue;
      var t = b[i + sc] & 0x1f;
      if (t === 5 || t === 7) return true;
      i += sc;
    }
    return false;
  }

  // Encoda a câmera e entrega cada access unit (Annex-B) via send().
  function createVideoSender(track, send) {
    var frameCount = 0, closed = false, forceKey = false;
    var encoder = new VideoEncoder({
      output: function (chunk) {
        var buf = new Uint8Array(chunk.byteLength);
        chunk.copyTo(buf);
        send(buf.buffer);
      },
      error: function (e) { console.error("video encoder error", e); },
    });
    encoder.configure({
      codec: VIDEO.CODEC, width: VIDEO.W, height: VIDEO.H, bitrate: VIDEO.BITRATE,
      framerate: VIDEO.FPS, latencyMode: "realtime", avc: { format: "annexb" },
    });
    var reader = new MediaStreamTrackProcessor({ track: track }).readable.getReader();
    function pump() {
      reader.read().then(function (res) {
        if (closed) return;
        var frame = res.value;
        if (res.done || !frame) return;
        if (encoder.encodeQueueSize < 2) {
          encoder.encode(frame, { keyFrame: forceKey || frameCount % VIDEO.KF === 0 });
          forceKey = false;
          frameCount++;
        }
        frame.close();
        pump();
      }).catch(function () {});
    }
    pump();
    return {
      forceKeyframe: function () { forceKey = true; },
      close: function () { closed = true; try { reader.cancel(); } catch (e) {} try { encoder.close(); } catch (e) {} },
    };
  }

  // Decoda os access units recebidos e expõe um MediaStream para um <video>.
  // onNeedKeyframe (opcional): chamado quando o decoder precisa de um keyframe pra
  // (re)começar — ainda não abriu, ou deu erro (frame corrompido por perda).
  function createVideoReceiver(onNeedKeyframe) {
    var generator = new MediaStreamTrackGenerator({ kind: "video" });
    var writer = generator.writable.getWriter();
    var stream = new MediaStream([generator]);
    var ts = 0, started = false, writing = false, closed = false, decoder = null;
    function recover() {
      if (closed) return;
      try { decoder.close(); } catch (e) {}
      decoder = newDecoder();
      started = false;
      if (onNeedKeyframe) onNeedKeyframe();
    }
    function newDecoder() {
      var d = new VideoDecoder({
        output: function (frame) {
          if (writing) { frame.close(); return; }
          writing = true;
          writer.write(frame).catch(function () { frame.close(); }).finally(function () { writing = false; });
        },
        error: function (e) { console.error("video decoder error", e); recover(); },
      });
      d.configure({ codec: VIDEO.CODEC, optimizeForLatency: true });
      return d;
    }
    decoder = newDecoder();
    return {
      stream: stream,
      decode: function (data) {
        if (closed) return;
        var bytes = new Uint8Array(data);
        var key = isAnnexBKeyframe(bytes);
        if (!started && !key) { if (onNeedKeyframe) onNeedKeyframe(); return; } // espera o primeiro keyframe
        started = true;
        var chunk = new EncodedVideoChunk({ type: key ? "key" : "delta", timestamp: ts, data: bytes });
        ts += 1000000 / VIDEO.FPS;
        try { decoder.decode(chunk); } catch (e) { console.error("video decode error", e); recover(); }
      },
      close: function () { closed = true; try { decoder.close(); } catch (e) {} try { writer.close(); } catch (e) {} },
    };
  }

  // Rotação de um <video>: automática pela orientação do celular (quartos de volta,
  // vinda do servidor) + ajuste manual (botão). Só gira sozinho quando o quadro
  // chega DEITADO e o aparelho diz que está em pé (1/3) — se já vem em pé, não mexe.
  function applyRotation(video, orientation, manual) {
    var landscape = video.videoWidth > 0 && video.videoWidth > video.videoHeight;
    var auto = (orientation % 2 === 1 && landscape) ? orientation * 90 : 0;
    var rot = (auto + manual) % 360;
    var sideways = rot === 90 || rot === 270;
    video.style.transform = "rotate(" + rot + "deg) scale(" + (sideways ? 1.34 : 1) + ")";
  }

  // ---------- vídeo em GRUPO (WS h264-group) ----------
  // Espelha client/src/lib/call/group-video.ts: câmera do atendente → WS; vídeo de
  // cada participante ← WS (um tile por pid); controle em JSON (keyframe_request,
  // video_state, orientation, participant_left); pedido de keyframe ("pli").
  async function openGroupVideo(session, labelFor) {
    var cam = await navigator.mediaDevices.getUserMedia({
      video: { width: { ideal: VIDEO.W }, height: { ideal: VIDEO.H }, frameRate: { ideal: VIDEO.FPS } },
      audio: false,
    });
    var track = cam.getVideoTracks()[0];
    var ws = new WebSocket(wsUrl("/api/sessions/" + session + "/calls/group/video-ws"), ["h264-group"]);
    ws.binaryType = "arraybuffer";
    var tiles = {}; // pid -> {rx, el, video, orientation, manual, lastAt}
    var lastPli = {}, sender = null, closed = false, staleTimer = null;

    function tilesEl() { return document.getElementById("wacalls-tiles"); }
    function requestPli(pid) {
      var now = Date.now();
      if (now - (lastPli[pid] || 0) < 1000) return;
      lastPli[pid] = now;
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "pli", pid: pid }));
    }
    function addTile(pid) {
      var t = { orientation: 0, manual: 0, lastAt: Date.now() };
      t.rx = createVideoReceiver(function () { requestPli(pid); });
      var d = el("div", "cw-tile");
      d.setAttribute("data-pid", pid);
      var v = document.createElement("video");
      v.autoplay = true; v.playsInline = true; v.muted = true;
      v.srcObject = t.rx.stream;
      v.onloadedmetadata = v.onresize = function () { applyRotation(v, t.orientation, t.manual); };
      var lbl = el("span", "cw-lbl", esc(labelFor(pid)));
      var rot = el("button", "cw-rot", ICON_ROTATE);
      rot.type = "button"; rot.title = "Girar";
      rot.onclick = function () { t.manual = (t.manual + 90) % 360; applyRotation(v, t.orientation, t.manual); };
      d.appendChild(v); d.appendChild(lbl); d.appendChild(rot);
      t.el = d; t.video = v;
      tiles[pid] = t;
      var c = tilesEl();
      if (c) { c.appendChild(d); c.style.display = "grid"; }
      v.play().catch(function () {});
      return t;
    }
    function removeTile(pid) {
      var t = tiles[pid];
      if (!t) return;
      delete tiles[pid];
      try { t.rx.close(); } catch (e) {}
      if (t.el && t.el.parentNode) t.el.parentNode.removeChild(t.el);
      delete lastPli[pid];
    }
    function relabel() {
      for (var pid in tiles) { var l = tiles[pid].el.querySelector(".cw-lbl"); if (l) l.textContent = labelFor(pid); }
    }
    ws.onmessage = function (ev) {
      if (typeof ev.data === "string") {
        var msg; try { msg = JSON.parse(ev.data); } catch (e) { return; }
        if (msg.type === "keyframe_request") { if (sender) sender.forceKeyframe(); }
        else if (msg.type === "orientation" && msg.pid) {
          var t = tiles[msg.pid] || addTile(msg.pid);
          t.orientation = msg.orientation || 0;
          applyRotation(t.video, t.orientation, t.manual);
        } else if ((msg.type === "video_state" && msg.pid && (msg.state === 0 || msg.state === 6)) || (msg.type === "participant_left" && msg.pid)) {
          removeTile(msg.pid);
        }
        return;
      }
      var bytes = new Uint8Array(ev.data);
      if (bytes.length < 2) return;
      var n = bytes[0];
      if (bytes.length < 1 + n) return;
      var pid = new TextDecoder().decode(bytes.subarray(1, 1 + n));
      var au = bytes.subarray(1 + n);
      var tile = tiles[pid] || addTile(pid);
      tile.lastAt = Date.now();
      tile.rx.decode(au.buffer.slice(au.byteOffset, au.byteOffset + au.byteLength));
    };
    await new Promise(function (resolve, reject) {
      ws.onopen = function () { resolve(); };
      ws.onerror = function () { reject(new Error("falha ao conectar o vídeo do grupo")); };
    });
    ws.onclose = function () { if (!closed && api_onclose) api_onclose(); };
    var api_onclose = null;
    // Tile 8s sem frame some (participante caiu sem sinalizar).
    staleTimer = setInterval(function () {
      var now = Date.now();
      for (var pid in tiles) if (now - tiles[pid].lastAt > 8000) removeTile(pid);
    }, 2000);
    function startSender() {
      if (sender || !track) return;
      sender = createVideoSender(track, function (au) { if (ws.readyState === WebSocket.OPEN) ws.send(au); });
    }
    startSender();
    // Preview da própria câmera ("Você") como primeiro tile.
    (function () {
      var d = el("div", "cw-tile");
      var v = document.createElement("video");
      v.autoplay = true; v.playsInline = true; v.muted = true;
      v.srcObject = cam;
      d.appendChild(v); d.appendChild(el("span", "cw-lbl", "Você"));
      var c = tilesEl();
      if (c) { c.appendChild(d); c.style.display = "grid"; }
      v.play().catch(function () {});
    })();
    return {
      localStream: cam,
      relabel: relabel,
      setCam: function (on) {
        if (on) { track.enabled = true; startSender(); }
        else { track.enabled = false; if (sender) { try { sender.close(); } catch (e) {} sender = null; } }
      },
      onClose: function (fn) { api_onclose = fn; },
      close: function () {
        if (closed) return; closed = true;
        if (staleTimer) clearInterval(staleTimer);
        try { if (sender) sender.close(); } catch (e) {}
        for (var pid in tiles) removeTile(pid);
        try { cam.getTracks().forEach(function (t) { t.stop(); }); } catch (e) {}
        try { ws.close(1000, "call ended"); } catch (e) {}
      },
    };
  }

  var NO_VIDEO = { remoteVideoStream: null, startSender: function () {}, stopSender: function () {}, close: function () {} };

  // Abre o datachannel "h264" (out-of-order) SEMPRE — mesmo em chamada de áudio —
  // para permitir vídeo mid-call sem renegociar SDP. Nunca derruba o áudio se falhar.
  function setupVideoChannel(pc) {
    if (!videoSupported()) return NO_VIDEO;
    try {
      var receiver = createVideoReceiver();
      var dc = pc.createDataChannel(VIDEO.LABEL, { ordered: false, maxRetransmits: 0 });
      dc.binaryType = "arraybuffer";
      dc.onmessage = function (e) { receiver.decode(e.data); };
      var sender = null, pending = null, open = false;
      function startNow(track) {
        if (sender) sender.close();
        sender = createVideoSender(track, function (au) { if (dc.readyState === "open") dc.send(au); });
      }
      dc.onopen = function () { open = true; if (pending) { startNow(pending); pending = null; } };
      return {
        remoteVideoStream: receiver.stream,
        startSender: function (track) { if (open) startNow(track); else pending = track; },
        stopSender: function () { try { if (sender) sender.close(); } catch (e) {} sender = null; pending = null; },
        close: function () { try { if (sender) sender.close(); } catch (e) {} try { receiver.close(); } catch (e) {} },
      };
    } catch (e) {
      console.warn("video channel setup failed; audio-only call", e);
      return NO_VIDEO;
    }
  }

  // Liga a câmera do widget e pede upgrade para o peer; ou desliga (downgrade).
  async function toggleCam(btn) {
    if (call && call.group) {
      if (!call.gv) return;
      call.localVideo = !call.localVideo;
      call.gv.setCam(call.localVideo);
      btn.innerHTML = call.localVideo ? ICON_VIDEO : ICON_VIDEO_OFF;
      btn.classList.toggle("on", call.localVideo);
      btn.title = call.localVideo ? "Desligar vídeo" : "Ligar vídeo";
      return;
    }
    if (!call || !call.video) return;
    if (call.ws) { setStatus("Vídeo indisponível (áudio via WebSocket)"); return; } // WS é áudio-only
    if (call.localVideo) {
      api("/api/sessions/" + call.session + "/calls/" + call.callId + "/video/stop", { method: "POST", body: {} }).catch(function () {});
      call.video.stopSender();
      if (call.camTrack) { try { call.camTrack.stop(); } catch (e) {} }
      call.camTrack = null;
      call.localVideo = false;
      updateVideoUI();
      return;
    }
    if (!videoSupported()) { setStatus("Vídeo não suportado neste navegador"); return; }
    try {
      var cam = await navigator.mediaDevices.getUserMedia({
        video: { width: { ideal: VIDEO.W }, height: { ideal: VIDEO.H }, frameRate: { ideal: VIDEO.FPS } },
      });
      call.camTrack = cam.getVideoTracks()[0] || null;
      if (!call.camTrack) return;
      call.video.startSender(call.camTrack);
      call.localVideo = true;
      updateVideoUI();
      await api("/api/sessions/" + call.session + "/calls/" + call.callId + "/video/request", { method: "POST", body: {} });
    } catch (e) {
      setStatus("Erro na câmera: " + (e.message || e));
    }
  }

  // Reflete o estado do vídeo na UI: mostra/oculta a área, anexa os streams,
  // atualiza o botão de câmera.
  function updateVideoUI() {
    if (!call) return;
    var area = document.getElementById("wacalls-video");
    var show = call.localVideo || call.peerVideo;
    if (area) area.style.display = show ? "block" : "none";
    var rv = document.getElementById("wacalls-remote-v");
    if (rv && call.video && call.video.remoteVideoStream && rv.srcObject !== call.video.remoteVideoStream) {
      rv.srcObject = call.video.remoteVideoStream;
      rv.onloadedmetadata = rv.onresize = function () { if (call) applyRotation(rv, call.peerOrientation || 0, call.manualRot || 0); };
      rv.play().catch(function () {});
    }
    if (rv) applyRotation(rv, call.peerOrientation || 0, call.manualRot || 0);
    var rb = document.getElementById("wacalls-rot");
    if (rb) rb.style.display = call.peerVideo ? "inline-flex" : "none";
    var lv = document.getElementById("wacalls-local-v");
    if (lv) {
      lv.style.display = call.localVideo ? "block" : "none";
      var want = call.localVideo && call.camTrack ? call.camTrack : null;
      if (want && (!lv.srcObject || lv.srcObject.getVideoTracks()[0] !== want)) {
        lv.srcObject = new MediaStream([want]);
        lv.play().catch(function () {});
      }
    }
    var cam = document.getElementById("wacalls-cam");
    if (cam) {
      cam.innerHTML = call.localVideo ? ICON_VIDEO : ICON_VIDEO_OFF;
      cam.classList.toggle("on", call.localVideo);
      cam.title = call.localVideo ? "Desligar vídeo" : "Ativar vídeo";
    }
  }

  async function startCall(state) {
    render({ inCall: true, name: state.name, phone: state.phone, status: "Conectando…" });
    try {
      await ensureConfig();
      var r = await api("/api/sessions/" + state.session + "/calls", {
        method: "POST",
        body: { phone: state.phone, duration_ms: 300000, record: false },
      });
      var callId = r.call.callId;
      if (pickTransport() === "websocket") {
        // WS: áudio-only, passa em proxy/sem UDP. Sem PC/offer/webrtc nem vídeo.
        var wsa = await openWSAudio(state.session, "/calls/" + callId + "/ws");
        call = { pc: null, ws: wsa, mic: wsa.mic, callId: callId, session: state.session, t0: null, timer: null, es: null, answered: false, video: NO_VIDEO, localVideo: false, peerVideo: false, camTrack: null };
      } else {
        var mic = await navigator.mediaDevices.getUserMedia({ audio: true });
        var pc = new RTCPeerConnection({ iceServers: [] });
        mic.getAudioTracks().forEach(function (t) { pc.addTrack(t, mic); });
        pc.addTransceiver("audio", { direction: "recvonly" });
        pc.ontrack = function (ev) {
          var a = document.getElementById("wacalls-audio");
          if (a && ev.streams[0]) { a.srcObject = ev.streams[0]; a.play().catch(function () {}); }
        };
        var video = setupVideoChannel(pc); // canal h264 sempre aberto (permite vídeo mid-call)
        var offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        await iceComplete(pc);
        var ans = await api("/api/sessions/" + state.session + "/calls/" + callId + "/webrtc", {
          method: "POST",
          body: { sdp_offer: pc.localDescription.sdp },
        });
        await pc.setRemoteDescription({ type: "answer", sdp: ans.sdp_answer });
        call = { pc: pc, ws: null, mic: mic, callId: callId, session: state.session, t0: null, timer: null, es: null, answered: false, video: video, localVideo: false, peerVideo: false, camTrack: null };
        pc.onconnectionstatechange = function () {
          // NÃO usar a conexão do navegador como "atendida" — ela conecta na hora
          // (navegador↔servidor), antes de o destinatário atender.
          if (pc.connectionState === "failed") setStatus("Falha na conexão");
        };
      }
      updateVideoUI();
      setStatus("Chamando…");
      // O tempo só começa quando o DESTINATÁRIO atende. O sinal real vem do
      // backend via SSE global (call-status "connected" = atendeu).
      connectEvents();
    } catch (e) {
      setStatus("Erro: " + (e.message || e));
    }
  }

  // Chamada em GRUPO: liga pro grupo da conversa (áudio, e vídeo opcional). O áudio
  // vai pelo WS de grupo (mix de todos ⇄ mic); o vídeo pelo WS h264-group (um tile
  // por participante). Encerramento remoto chega como fechamento do WS.
  async function startGroupCall(state, withVideo) {
    if (call) return;
    if (withVideo && !videoSupported()) { render({ error: "Vídeo não suportado neste navegador" }); return; }
    render({ inGroupCall: true, name: state.name, video: withVideo, status: "Conectando…" });
    try {
      var r = await api("/api/sessions/" + state.session + "/calls/group", {
        method: "POST", body: { groupJid: state.groupJid, video: !!withVideo },
      });
      var roster = {};
      function labelFor(pid) {
        var num = pid.split(/[:@]/)[0];
        var e = roster[num];
        if (!e) return pid.slice(-4);
        var phone = e.phone ? "+" + e.phone : "";
        return e.name && phone ? e.name + " · " + phone : e.name || phone || pid.slice(-4);
      }
      var wsa = await openWSAudio(state.session, "/calls/group/ws", function () { if (call && call.group) hangup(); });
      call = { group: true, pc: null, ws: wsa, mic: wsa.mic, callId: r.callId, session: state.session, t0: null, timer: null, es: null, answered: false, video: NO_VIDEO, localVideo: !!withVideo, peerVideo: false, camTrack: null, gv: null };
      api("/api/sessions/" + state.session + "/calls/group/roster").then(function (x) {
        roster = (x && x.roster) || {};
        if (call && call.gv) call.gv.relabel();
      }).catch(function () {});
      if (withVideo) {
        try {
          call.gv = await openGroupVideo(state.session, labelFor);
          call.gv.onClose(function () { if (call && call.group) hangup(); });
        } catch (e) {
          setStatus("Vídeo indisponível: " + (e.message || e));
        }
      }
      call.answered = true;
      call.t0 = Date.now();
      setStatus("Em chamada de grupo");
      call.timer = setInterval(tick, 1000);
    } catch (e) {
      if (call && call.group) { var c = call; call = null; try { c.ws && c.ws.close(); } catch (_) {} try { c.gv && c.gv.close(); } catch (_) {} }
      api("/api/sessions/" + state.session + "/calls/group/end", { method: "POST", body: {} }).catch(function () {});
      setStatus("Erro: " + (e.message || e));
    }
  }

  // Acompanha o estado real da chamada pelo SSE do backend. Só quando o
  // destinatário ATENDE (status "connected") é que começa o cronômetro e
  // aparece "Em chamada"; enquanto isso mostra "Chamando…".
  // SSE persistente: acompanha o estado da chamada ativa E detecta chamadas
  // recebidas (evento "incoming") pra abrir o widget no Chatwoot e tocar.
  var esAccount = null; // conta do Chatwoot com que o SSE está conectado

  // conta do Chatwoot atual, extraída da URL (/accounts/<N>/...). O widget SEMPRE
  // declara sua conta no SSE para receber só as chamadas da própria empresa —
  // sem isso o backend não teria como escopar e tocaria em todas.
  function currentAccountId() {
    var m = location.pathname.match(/accounts\/(\d+)/);
    return m ? m[1] : null;
  }

  function connectEvents() {
    if (!BASE) return;
    var acc = currentAccountId();
    if (!acc) return; // fora de uma conta do Chatwoot: nada a escutar ainda
    if (globalES && esAccount === acc) return; // já conectado nesta conta
    if (globalES) { try { globalES.close(); } catch (e) {} globalES = null; }
    esAccount = acc;
    try {
      var q = "?accountId=" + encodeURIComponent(acc) + (KEY ? "&apiKey=" + encodeURIComponent(KEY) : "");
      globalES = new EventSource(BASE + "/api/events" + q);
      globalES.onmessage = function (ev) {
        var msg;
        try { msg = JSON.parse(ev.data); } catch (e) { return; }
        handleEvent(msg);
      };
      globalES.onerror = function () {}; // EventSource reconecta sozinho
    } catch (e) {}
  }

  function handleEvent(msg) {
    // chamada ativa (saída, ou entrada já aceita): controla cronômetro/fim
    if (call && msg.id === call.callId) {
      if (msg.type === "call-ended" || msg.status === "ended") hangup();
      else if (msg.type === "video-state") {
        call.peerVideo = !!msg.peerVideo;
        call.peerOrientation = msg.peerOrientation || 0;
        // o peer pediu vídeo: aceita para receber (câmera nossa só se o usuário ligar)
        if (msg.upgradeIncoming) {
          api("/api/sessions/" + call.session + "/calls/" + call.callId + "/video/accept", { method: "POST", body: {} }).catch(function () {});
        }
        updateVideoUI();
      } else if (msg.type === "call-status") {
        if (msg.status === "connected") markAnswered();
        else if (!call.answered) setStatus("Chamando…");
      }
      return;
    }
    // chamada RECEBIDA chegando → abre o widget e toca
    if (msg.type === "incoming") {
      if (call) return; // já em chamada
      if (incoming && incoming.callId === msg.id) return; // já tocando esta chamada
      // phone/name resolvidos pelo backend (issue #9); peer (LID cru) só como último fallback
      var incPhone = msg.phone || msg.peer || "";
      incoming = { sessionId: msg.sessionId, callId: msg.id, peer: incPhone, name: msg.name || "", video: !!msg.video };
      render({ incoming: true, phone: incoming.name ? incoming.name + " · " + incPhone : incPhone });
      playRing();
      return;
    }
    // a recebida pendente encerrou antes de atender (desistiu)
    if (incoming && msg.id === incoming.callId && (msg.type === "call-ended" || msg.status === "ended")) {
      incoming = null;
      stopRing();
      var p = document.getElementById("wacalls-panel");
      if (p) p.remove();
    }
  }

  async function acceptIncoming() {
    var inc = incoming;
    if (!inc) return;
    incoming = null;
    stopRing();
    render({ inCall: true, name: "Chamada recebida", phone: inc.peer, status: "Conectando…" });
    try {
      await ensureConfig();
      await api("/api/sessions/" + inc.sessionId + "/calls/" + inc.callId + "/accept", { method: "POST", body: {} });
      if (pickTransport() === "websocket") {
        var wsa = await openWSAudio(inc.sessionId, "/calls/" + inc.callId + "/ws");
        call = { pc: null, ws: wsa, mic: wsa.mic, callId: inc.callId, session: inc.sessionId, t0: null, timer: null, es: null, answered: false, video: NO_VIDEO, localVideo: false, peerVideo: false, camTrack: null };
        markAnswered();
        updateVideoUI();
      } else {
        var mic = await navigator.mediaDevices.getUserMedia({ audio: true });
        var pc = new RTCPeerConnection({ iceServers: [] });
        mic.getAudioTracks().forEach(function (t) { pc.addTrack(t, mic); });
        pc.addTransceiver("audio", { direction: "recvonly" });
        pc.ontrack = function (ev) {
          var a = document.getElementById("wacalls-audio");
          if (a && ev.streams[0]) { a.srcObject = ev.streams[0]; a.play().catch(function () {}); }
        };
        var video = setupVideoChannel(pc); // canal h264 sempre aberto
        var offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        await iceComplete(pc);
        var ans = await api("/api/sessions/" + inc.sessionId + "/calls/" + inc.callId + "/webrtc", { method: "POST", body: { sdp_offer: pc.localDescription.sdp } });
        await pc.setRemoteDescription({ type: "answer", sdp: ans.sdp_answer });
        call = { pc: pc, ws: null, mic: mic, callId: inc.callId, session: inc.sessionId, t0: null, timer: null, es: null, answered: false, video: video, localVideo: false, peerVideo: !!inc.video, camTrack: null };
        markAnswered();
        updateVideoUI();
        // Se a chamada recebida já é de vídeo, liga a câmera automaticamente.
        if (inc.video) toggleCam(document.getElementById("wacalls-cam"));
      }
    } catch (e) {
      setStatus("Erro: " + (e.message || e));
      try { await api("/api/sessions/" + inc.sessionId + "/calls/" + inc.callId, { method: "DELETE" }); } catch (_) {}
    }
  }

  function rejectIncoming() {
    var inc = incoming;
    incoming = null;
    stopRing();
    if (inc) api("/api/sessions/" + inc.sessionId + "/calls/" + inc.callId + "/reject", { method: "POST", body: {} }).catch(function () {});
    var p = document.getElementById("wacalls-panel");
    if (p) p.remove();
  }

  function markAnswered() {
    if (!call || call.answered) return;
    stopRing(); // garante que o apito de "tocando" pare ao atender
    call.answered = true;
    call.t0 = Date.now();
    setStatus("Em chamada");
    call.timer = setInterval(tick, 1000);
  }

  function tick() {
    if (!call || !call.t0) return;
    var s = Math.floor((Date.now() - call.t0) / 1000);
    var mm = String(Math.floor(s / 60)).padStart(2, "0");
    var ss = String(s % 60).padStart(2, "0");
    var st = document.getElementById("wacalls-st");
    if (st && st.textContent.indexOf("Erro") < 0) st.textContent = mm + ":" + ss;
  }

  function toggleMute(btn) {
    if (!call) return;
    var tracks = call.mic.getAudioTracks();
    var on = tracks.length && tracks[0].enabled;
    tracks.forEach(function (t) {
      t.enabled = !on;
    });
    // on=true significa que estava ligado e agora fica mudo
    btn.innerHTML = on ? ICON_MIC_OFF : ICON_MIC;
    btn.classList.toggle("on", on);
    btn.title = on ? "Ativar microfone" : "Mudo";
  }

  function hangup() {
    if (!call) return;
    var c = call;
    call = null;
    if (c.timer) clearInterval(c.timer);
    if (c.es) try { c.es.close(); } catch (e) {}
    if (c.group) {
      api("/api/sessions/" + c.session + "/calls/group/end", { method: "POST", body: {} }).catch(function () {});
      try { if (c.gv) c.gv.close(); } catch (e) {}
    } else {
      api("/api/sessions/" + c.session + "/calls/" + c.callId, { method: "DELETE" }).catch(function () {});
    }
    try {
      c.mic.getTracks().forEach(function (t) {
        t.stop();
      });
    } catch (e) {}
    try { if (c.video) c.video.close(); } catch (e) {}
    try { if (c.camTrack) c.camTrack.stop(); } catch (e) {}
    try { if (c.ws) c.ws.close(); } catch (e) {}
    try {
      if (c.pc) c.pc.close();
    } catch (e) {}
    closePanel();
  }

  // ---------- vínculo empresa+caixa ----------
  // O ícone só aparece quando a conversa aberta pertence a uma conta E caixa (inbox)
  // que tem uma sessão AstraCalls conectada. O backend (/chatwoot/resolve) é quem decide:
  // ele descobre o inbox_id da conversa e só responde 200 se houver sessão amarrada.
  var currentConvKey = null; // "acc/conv" da conversa atual
  var callable = false; // a conversa atual é de uma caixa conectada?
  var resolved = null; // cache {session, phone, name}

  // Monta o estado do painel a partir do /chatwoot/resolve: contato 1:1 (phone) ou
  // GRUPO (group_jid → botões de chamada em grupo).
  function resolvedFrom(info) {
    if (info.group) {
      return { session: info.session_id, group: true, groupJid: info.group_jid, groupCalls: !!info.group_calls, name: info.name || "Grupo", phone: "" };
    }
    return { session: info.session_id, phone: info.phone, name: info.name || info.phone };
  }

  function convKey() {
    var acc = location.pathname.match(/accounts\/(\d+)/);
    var conv = location.pathname.match(/conversations\/(\d+)/);
    return acc && conv ? acc[1] + "/" + conv[1] : null;
  }

  function refreshBinding() {
    var key = convKey();
    if (key === currentConvKey) return; // mesma conversa: nada a fazer
    currentConvKey = key;
    callable = false;
    resolved = null;
    var b = document.getElementById("wacalls-btn");
    if (b) b.remove();
    if (!key) return;
    var parts = key.split("/");
    api("/api/chatwoot/resolve?account_id=" + parts[0] + "&conversation_id=" + parts[1])
      .then(function (info) {
        if (convKey() !== key) return; // o agente já trocou de conversa
        resolved = resolvedFrom(info);
        callable = true;
        ensureButton();
      })
      .catch(function () {
        if (convKey() !== key) return;
        callable = false;
        var x = document.getElementById("wacalls-btn");
        if (x) x.remove();
      });
  }

  var WARN_MSG =
    "Esta conversa não pertence à caixa de entrada conectada ao WhatsApp. " +
    "Abra uma conversa da caixa conectada para ligar. " +
    "(Se o ícone apareceu aqui por engano, é cache do Chatwoot — atualize a página.)";

  function onCall() {
    console.log("[wacalls-widget] clique no botão de ligar");
    if (resolved) {
      render(resolved);
      return;
    }
    // Sem vínculo em cache: confirma ao vivo (o ícone pode ter sobrado por cache do Chatwoot).
    var key = convKey();
    if (!key) {
      render({ warn: "Abra uma conversa para ligar." });
      return;
    }
    render({ loading: true });
    var parts = key.split("/");
    api("/api/chatwoot/resolve?account_id=" + parts[0] + "&conversation_id=" + parts[1])
      .then(function (info) {
        resolved = resolvedFrom(info);
        callable = true;
        render(resolved);
      })
      .catch(function () {
        callable = false;
        render({ warn: WARN_MSG });
      });
  }

  // ---------- injeção do ícone (container de ações do header da conversa) ----------
  // Técnica: acha botões quadrados (~32px) no lado direito, cujo pai tem 2-6 botões
  // (= barra de ações do ticket). Fallback pelo texto "Ações da conversa".
  function findActionsContainer() {
    if (ANCHOR) {
      var a = document.querySelector(ANCHOR);
      if (a) return { container: a, sibling: a.querySelector("button") || a };
    }
    var btns = document.querySelectorAll("button");
    for (var i = 0; i < btns.length; i++) {
      var b = btns[i];
      var r = b.getBoundingClientRect();
      if (r.width >= 28 && r.width <= 40 && r.height >= 28 && r.height <= 40 && r.left > window.innerWidth * 0.6) {
        var p = b.parentElement;
        if (p) {
          var sib = p.querySelectorAll(":scope > button");
          if (sib.length >= 2 && sib.length <= 6) return { container: p, sibling: b };
        }
      }
    }
    var spans = document.querySelectorAll("span");
    for (var j = 0; j < spans.length; j++) {
      if (spans[j].textContent.trim() === "Ações da conversa") {
        var section = spans[j].closest("div");
        if (section && section.parentElement) {
          var divs = section.parentElement.querySelectorAll("div");
          for (var k = 0; k < divs.length; k++) {
            var bb = divs[k].querySelectorAll(":scope > button");
            if (bb.length >= 2 && bb.length <= 6) return { container: divs[k], sibling: bb[0] };
          }
        }
      }
    }
    return null;
  }

  function ensureButton() {
    // só injeta se a conversa atual for de uma caixa conectada (vínculo empresa+caixa)
    if (!callable || !/\/conversations\/\d+/.test(location.pathname)) {
      var old = document.getElementById("wacalls-btn");
      if (old) old.remove();
      return;
    }
    if (document.getElementById("wacalls-btn")) return;
    var found = findActionsContainer();
    if (!found || !found.container) return;
    if (found.container.querySelector("#wacalls-btn")) return;
    var btn = document.createElement("button");
    btn.id = "wacalls-btn";
    btn.type = "button";
    btn.title = resolved && resolved.group ? "Ligar para o grupo pelo WhatsApp" : "Ligar pelo WhatsApp";
    btn.className = found.sibling && found.sibling.className
      ? found.sibling.className // herda o estilo nativo do Chatwoot
      : "inline-flex items-center justify-center h-8 w-8 p-0 rounded-lg";
    btn.innerHTML = PHONE_SVG;
    btn.onclick = function (e) {
      e.preventDefault();
      e.stopPropagation();
      onCall();
    };
    found.container.appendChild(btn);
    console.log("[wacalls-widget] ícone injetado no container de ações da conversa");
  }

  var obs = new MutationObserver(function () {
    refreshBinding();
    ensureButton();
  });
  obs.observe(document.body, { childList: true, subtree: true });
  // verifica troca de conversa/conta também por timer (a URL muda sem alterar o DOM
  // às vezes); connectEvents reconecta o SSE se o agente trocou de conta.
  setInterval(function () {
    refreshBinding();
    connectEvents();
  }, 1000);
  var tries = 0;
  (function retry() {
    refreshBinding();
    ensureButton();
    if (++tries < 40) setTimeout(retry, 800);
  })();
  connectEvents(); // SSE sempre ligado p/ receber chamadas mesmo sem painel aberto
  console.log("[wacalls-widget] carregado. base=", BASE);
})();
