(function(){
"use strict";
var $ = function(s, el){ return (el||document).querySelector(s); };
var $$ = function(s, el){ return Array.prototype.slice.call((el||document).querySelectorAll(s)); };
var esc = function(t){ var d=document.createElement("div"); d.textContent=t; return d.innerHTML; };
var nowTime = function(){ var d=new Date(); return ("0"+d.getHours()).slice(-2)+":"+("0"+d.getMinutes()).slice(-2); };

var settings = { stream:true, sound:false, follow:true };
var deepThink = true;
var verbosity = "balanced";
var mode = "chat"; /* chat | agent | code */
var sessionStart = nowTime();
$$(".set-info-start").forEach(function(el){ el.textContent = sessionStart; });

/* Toast */
var toastEl = $("#toast"), toastTimer = null;
function toast(msg){
  toastEl.innerHTML = '<svg class="tk" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>' + esc(msg);
  toastEl.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(function(){ toastEl.classList.remove("show"); }, 2600);
}

/* Sound */
var audioCtx = null;
function beep(){
  if(!settings.sound) return;
  try{
    if(!audioCtx) audioCtx = new (window.AudioContext||window.webkitAudioContext)();
    if(audioCtx.state==="suspended") audioCtx.resume();
    var o = audioCtx.createOscillator(), g = audioCtx.createGain();
    o.frequency.value = 880; o.type = "sine";
    g.gain.setValueAtTime(0.0001, audioCtx.currentTime);
    g.gain.exponentialRampToValueAtTime(0.08, audioCtx.currentTime + 0.02);
    g.gain.exponentialRampToValueAtTime(0.0001, audioCtx.currentTime + 0.25);
    o.connect(g); g.connect(audioCtx.destination);
    o.start(); o.stop(audioCtx.currentTime + 0.3);
  }catch(e){}
}

/* Activity (session context) */
var activityItems = [];
function renderActivity(){
  var wrap = $("#activityList"); wrap.innerHTML = "";
  if(!activityItems.length){ wrap.innerHTML = '<div class="ctx-note">Chưa có hoạt động nào trong phiên này.</div>'; return; }
  activityItems.forEach(function(it){
    var el = document.createElement("div");
    el.className = "act-item";
    el.innerHTML = '<span style="flex:1;min-width:0"><span class="at">'+esc(it.t)+'</span><span class="as">'+esc(it.s)+'</span></span><span style="font-size:12px;color:var(--ink-2);font-weight:600;flex:none">'+esc(it.time)+'</span>';
    wrap.appendChild(el);
  });
}
function addActivity(title, sub){
  activityItems.unshift({ t:title, s:sub, time:nowTime() });
  if(activityItems.length > 6) activityItems.pop();
  renderActivity();
  $("#ctxNote").textContent = "Chủ đề gần nhất: “" + title + "”. Mart đang ở chế độ " + modeLabel() + ", độ chi tiết " + verbosityLabel() + (deepThink ? ", suy luận sâu đang bật." : ".");
}
$("#clearActivity").addEventListener("click", function(){ activityItems = []; renderActivity(); toast("Đã xoá hoạt động trong phiên"); });
function modeLabel(){ return mode==="agent" ? "Agent" : (mode==="code" ? "Code" : "Chat"); }
function verbosityLabel(){ return verbosity==="short" ? "ngắn gọn" : (verbosity==="detailed" ? "chi tiết" : "cân bằng"); }

/* Usage */
var msgCount = 0, tokenEst = 0;
var CTX_LIMIT = 32000;
function updateUsage(){
  $("#usageMsgs").textContent = msgCount;
  $("#usageTokens").textContent = tokenEst.toString().replace(/\B(?=(\d{3})+(?!\d))/g,".");
  var pct = Math.min(100, Math.round(tokenEst / CTX_LIMIT * 100));
  $("#usageFill").style.width = pct + "%";
  $("#usageNote").textContent = "Ngữ cảnh phiên: " + pct + "% — " + (pct < 50 ? "còn rất rộng." : (pct < 85 ? "đang đầy dần, cân nhắc mở phiên mới nếu đổi chủ đề." : "sắp đầy, nên xuất bản ghi rồi mở phiên mới."));
}
function countTokens(text){ return Math.max(1, Math.round(text.split(/\s+/).filter(Boolean).length * 1.35)); }

/* History */
var CONVERSATIONS = [];
var activeConvId = null;
var ICON_MSG = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5Z"/></svg>';
function renderHistory(filter){
  var wrap = $("#history"); wrap.innerHTML = "";
  var q = (filter||"").trim().toLowerCase();
  var groups = ["Hôm nay","Hôm qua","Trước đó"];
  var any = false;
  groups.forEach(function(g){
    var items = CONVERSATIONS.filter(function(c){
      return c.group===g && (!q || c.title.toLowerCase().indexOf(q)>-1 || c.preview.toLowerCase().indexOf(q)>-1);
    });
    if(!items.length) return;
    any = true;
    var gd = document.createElement("div");
    gd.className = "hist-group";
    gd.innerHTML = '<div class="side-label" style="padding:0 10px">'+g+'</div>';
    items.forEach(function(c){
      var b = document.createElement("button");
      b.className = "hist-item" + (c.id===activeConvId ? " on" : "");
      b.innerHTML = '<span class="hist-ic">'+ICON_MSG+'</span><span class="hist-main"><span class="hist-t">'+esc(c.title)+'</span><span class="hist-s" style="display:block">'+esc(c.preview)+'</span></span><span class="hist-time">'+esc(c.time)+'</span>' +
        '<span class="hist-del" data-del="1" role="button" aria-label="Xoá đoạn chat"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/></svg></span>';
      b.addEventListener("click", function(e){
        if(e.target.closest("[data-del]")){
          e.stopPropagation();
          CONVERSATIONS = CONVERSATIONS.filter(function(x){ return x.id!==c.id; });
          if(activeConvId===c.id){ activeConvId = null; resetChat(); }
          renderHistory($("#searchHist").value);
          toast("Đã xoá đoạn chat");
          return;
        }
        openConversation(c);
      });
      gd.appendChild(b);
    });
    wrap.appendChild(gd);
  });
  if(!any) wrap.innerHTML = '<div class="hist-empty">Không tìm thấy đoạn chat nào.</div>';
}
$("#searchHist").addEventListener("input", function(){ renderHistory(this.value); });
function saveToHistory(title, preview){
  var c = { id:"c"+Date.now(), group:"Hôm nay", title:title, preview:preview, time:nowTime(), msgs:null, live:true };
  CONVERSATIONS.unshift(c);
  activeConvId = c.id;
  renderHistory($("#searchHist").value);
  return c;
}

/* Chat rendering */
var messagesEl = $("#messages"), welcomeEl = $("#welcome"), scrollEl = $("#chatScroll");
var input = $("#composerInput"), sendBtn = $("#sendBtn"), stopBtn = $("#stopBtn"), charCount = $("#charCount");
var started = false, streaming = null, lastUserText = "";

var MASCOT_HTML = '<span class="mascot"><img src="data:image/webp;base64,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" alt="" draggable="false"/></span>';
var ICON_COPY = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
var ICON_LIKE = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M7 10v12H4a1 1 0 0 1-1-1V11a1 1 0 0 1 1-1h3Zm0 0 4.2-7.4c1.5 0 2.6 1.3 2.3 2.8L12.6 10H20a2 2 0 0 1 2 2.4l-1.2 7.2a2 2 0 0 1-2 1.6H7"/></svg>';
var ICON_DISLIKE = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="transform:rotate(180deg)"><path d="M7 10v12H4a1 1 0 0 1-1-1V11a1 1 0 0 1 1-1h3Zm0 0 4.2-7.4c1.5 0 2.6 1.3 2.3 2.8L12.6 10H20a2 2 0 0 1 2 2.4l-1.2 7.2a2 2 0 0 1-2 1.6H7"/></svg>';
var ICON_REGEN = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.6-6.4M21 3v6h-6"/></svg>';

function scrollDown(){ requestAnimationFrame(function(){ scrollEl.scrollTop = scrollEl.scrollHeight; }); }
function actionsBar(getText, canRegen){
  var bar = document.createElement("div");
  bar.className = "m-actions";
  bar.innerHTML = '<button data-a="copy" aria-label="Sao chép" title="Sao chép">'+ICON_COPY+'</button>' +
    '<button data-a="like" aria-label="Hữu ích" title="Hữu ích">'+ICON_LIKE+'</button>' +
    '<button data-a="dislike" aria-label="Chưa tốt" title="Chưa tốt">'+ICON_DISLIKE+'</button>' +
    (canRegen ? '<button data-a="regen" aria-label="Trả lời lại" title="Trả lời lại">'+ICON_REGEN+'</button>' : '');
  bar.addEventListener("click", function(e){
    var b = e.target.closest("button"); if(!b) return;
    var a = b.getAttribute("data-a");
    if(a==="copy"){
      if(navigator.clipboard) navigator.clipboard.writeText(getText()).catch(function(){});
      toast("Đã sao chép tin nhắn");
    }
    if(a==="like"){ b.classList.toggle("liked"); var d = bar.querySelector('[data-a="dislike"]'); if(d) d.classList.remove("disliked"); if(b.classList.contains("liked")) toast("Cảm ơn phản hồi của bạn"); }
    if(a==="dislike"){ b.classList.toggle("disliked"); var l = bar.querySelector('[data-a="like"]'); if(l) l.classList.remove("liked"); if(b.classList.contains("disliked")) toast("Đã ghi nhận — Mart sẽ trả lời tốt hơn"); }
    if(a==="regen"){ if(lastUserText) handleSend(lastUserText); }
  });
  return bar;
}
function followupsBar(list){
  if(!settings.follow || !list || !list.length) return null;
  var row = document.createElement("div");
  row.className = "followups";
  list.forEach(function(q){
    var b = document.createElement("button");
    b.className = "chip-q"; b.type = "button"; b.textContent = q;
    b.addEventListener("click", function(){ handleSend(q); });
    row.appendChild(b);
  });
  return row;
}
function addUserMsg(text){
  var wrap = document.createElement("div");
  wrap.className = "msg user";
  wrap.innerHTML = '<div class="m-av">S</div><div class="m-body"><div class="bubble"></div></div>';
  wrap.querySelector(".bubble").textContent = text;
  messagesEl.appendChild(wrap);
  msgCount++; tokenEst += countTokens(text);
  updateUsage();
  scrollDown();
}
function stripTags(html){ var d = document.createElement("div"); d.innerHTML = html; return d.textContent; }
function wireCodeCopy(scope){
  $$(".code-copy", scope).forEach(function(btn){
    btn.addEventListener("click", function(){
      var pre = btn.closest(".code-block").querySelector("code");
      if(navigator.clipboard && pre) navigator.clipboard.writeText(pre.textContent).catch(function(){});
      var old = btn.textContent; btn.textContent = "Đã chép ✓";
      toast("Đã sao chép code");
      setTimeout(function(){ btn.textContent = old; }, 1600);
    });
  });
}
function addAiMsg(html, followups, reasoningHtml){
  var wrap = document.createElement("div");
  wrap.className = "msg ai";
  wrap.innerHTML = '<div class="m-av">' + MASCOT_HTML + '</div><div class="m-body">' +
    '<div class="m-name">Mart AI · '+currentModelShort()+' <span>'+nowTime()+'</span></div></div>';
  var body = wrap.querySelector(".m-body");
  if(reasoningHtml && deepThink && currentModelId!=="fast"){
    var det = document.createElement("details");
    det.className = "reasoning";
    det.innerHTML = "<summary>Quá trình suy luận</summary><div class='r-body'>" + reasoningHtml + "</div>";
    body.appendChild(det);
  }
  var bubble = document.createElement("div");
  bubble.className = "bubble";
  body.appendChild(bubble);
  messagesEl.appendChild(wrap);
  scrollDown();
  var plain = stripTags(html);
  msgCount++; tokenEst += countTokens(plain);
  updateUsage();
  var streamed = !!settings.stream;
  var liveEl = null;
  function finish(){
    document.body.classList.remove("is-thinking");
    document.body.classList.remove("is-working");
    wrap.classList.remove("is-live");
    if(liveEl){ liveEl.remove(); liveEl = null; }
    if(streamed){
      var av = wrap.querySelector(".m-av");
      if(av){ av.classList.add("celebrate"); setTimeout(function(){ av.classList.remove("celebrate"); }, 1350); }
    }
    wireCodeCopy(body);
    body.appendChild(actionsBar(function(){ return plain; }, true));
    var fu = followupsBar(followups);
    if(fu) body.appendChild(fu);
    beep();
    scrollDown();
  }
  if(!settings.stream){
    bubble.innerHTML = html;
    finish();
    return wrap;
  }
  var tokens = plain.split(/(\s+)/);
  var i = 0;
  bubble.innerHTML = "";
  document.body.classList.remove("is-thinking");
  document.body.classList.add("is-working");
  wrap.classList.add("is-live");
  liveEl = document.createElement("span");
  liveEl.className = "m-live";
  liveEl.innerHTML = 'Đang làm việc<span class="live-dots"><i></i><i></i><i></i></span>';
  var nameEl = wrap.querySelector(".m-name");
  if(nameEl) nameEl.appendChild(liveEl);
  var caret = document.createElement("span"); caret.className = "caret";
  stopBtn.classList.add("show"); sendBtn.style.display = "none";
  streaming = { cancelled:false };
  var myStream = streaming;
  (function step(){
    if(myStream.cancelled){
      bubble.textContent = plain;
      streaming = null;
      stopBtn.classList.remove("show"); sendBtn.style.display = "";
      finish();
      return;
    }
    if(i >= tokens.length){
      bubble.innerHTML = html;
      streaming = null;
      stopBtn.classList.remove("show"); sendBtn.style.display = "";
      finish();
      return;
    }
    bubble.textContent = tokens.slice(0, i+2).join("");
    bubble.appendChild(caret);
    i += 2;
    scrollDown();
    setTimeout(step, verbosity==="short" ? 14 : 24);
  })();
  return wrap;
}
var workPhaseTimer = null;
var workPhaseSetter = null;
function updateTypingProgress(stage, detail){ if(workPhaseSetter) workPhaseSetter(stage, detail); }
function showTyping(){
  var wrap = document.createElement("div");
  wrap.className = "msg ai work-row"; wrap.id = "typingRow";
  var phases = ["\u0110ang g\u1eedi c\u00e2u h\u1ecfi t\u1edbi m\u00e1y ch\u1ee7 Mart AI...", "\u0110\u00e3 nh\u1eadn ph\u1ea3n h\u1ed3i t\u1eeb m\u00e1y ch\u1ee7; \u0111ang chuy\u1ec3n t\u1edbi Muse.ai...", "\u0110ang ch\u1edd Muse.ai tr\u1ea3 l\u1eddi qua Chrome..."];
  var stepLabels = ["G\u1eedi c\u00e2u h\u1ecfi t\u1edbi m\u00e1y ch\u1ee7 Mart AI", "Mart AI chuy\u1ec3n c\u00e2u h\u1ecfi t\u1edbi Muse.ai", "Nh\u1eadn c\u00e2u tr\u1ea3 l\u1eddi t\u1eeb Muse.ai"];
  var stepsHtml = stepLabels.map(function(s, idx){ return '<li data-step="' + idx + '"><span class="ws-ic"></span><span>' + esc(s) + '</span></li>'; }).join("");
  wrap.innerHTML = '<div class="work-stage"><img class="work-avatar" src="/avatars/mart_cat.png" alt="" width="56" height="56" decoding="async"></div>' +
    '<div class="m-body"><div class="m-name">Mart AI · '+currentModelShort()+'</div>' +
    '<div class="work-card"><div class="work-badge"><span class="bw-pulse"></span>MART ĐANG LÀM VIỆC</div>' +
    '<div class="work-status"><span class="work-phase">'+phases[0]+'</span><span class="live-dots"><i></i><i></i><i></i></span></div>' +
    '<div class="work-sub">Model '+currentModelShort()+' · Chế độ '+modeLabel()+' · Thao tác đang chạy ngay bên dưới logo</div>' +
    '<ul class="work-steps">' + stepsHtml + '</ul>' +
    '<div class="work-bar"><i></i></div></div></div>';
  document.body.classList.add("is-thinking");
  messagesEl.appendChild(wrap); scrollDown();
  var phaseEl = wrap.querySelector(".work-phase"), pi = 0;
  var stepEls = $$(".work-steps li", wrap);
  var CHECK = '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>';
  function paintSteps(){
    stepEls.forEach(function(li, idx){
      li.classList.toggle("done", idx < pi);
      li.classList.toggle("active", idx === pi);
      li.querySelector(".ws-ic").innerHTML = idx < pi ? CHECK : (idx === pi ? '<span class="spinner ws-spinner"></span>' : String(idx + 1));
    });
  }
  paintSteps();
  if(workPhaseTimer) clearInterval(workPhaseTimer);
  workPhaseTimer = null;
  workPhaseSetter = function(stage, detail){
    pi = Math.max(0, Math.min(stepEls.length - 1, Number(stage) || 0));
    if(phaseEl) phaseEl.textContent = detail || phases[pi];
    paintSteps();
    var bar = wrap.querySelector(".work-bar i");
    if(bar) bar.style.width = Math.round(((pi + 1) / stepEls.length) * 100) + "%";
  };
  workPhaseSetter(0, phases[0]);
}
function hideTyping(){ if(workPhaseTimer){ clearInterval(workPhaseTimer); workPhaseTimer = null; } workPhaseSetter = null; var t = $("#typingRow"); if(t) t.remove(); }

function startConversation(){
  if(!started){ started = true; welcomeEl.style.display = "none"; }
}

function setTitle(t){
  var el = $("#chatTitle");
  if(el) el.textContent = t;
}

var FLOWS = {
  code:    { title:"Hàm debounce trong JavaScript", user:"Viết hàm debounce trong JavaScript giúp mình, kèm ví dụ sử dụng" },
  write:   { title:"Viết email xin nghỉ phép", user:"Viết giúp mình email xin nghỉ phép lịch sự" },
  analyze: { title:"So sánh React và Vue", user:"So sánh React và Vue giúp mình, nên chọn cái nào?" },
  explain: { title:"Giải thích đệ quy cho người mới", user:"Giải thích đệ quy cho người mới giúp mình, dễ hiểu thôi" }
};

function runFlow(key){
  var f = FLOWS[key]; if(!f) return;
  closeDrawers();
  handleSend(f.user);
}

stopBtn.addEventListener("click", function(){
  if(streaming){
    streaming.cancelled = true;
    if(streaming.controller) streaming.controller.abort();
  }
});

/* ===== LOGIC KẾT NỐI THẬT VỚI MART AI BACKEND (100% CHAT STREAMING & THREADS THẬT) ===== */

function wireCodeCopy(scope){
  $$( ".copy-code-btn", scope).forEach(function(btn){
    btn.onclick = function(){
      var pre = btn.closest(".code-wrap").querySelector("pre");
      if(!pre) return;
      navigator.clipboard.writeText(pre.textContent).then(function(){
        btn.innerHTML = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg> Đã sao chép';
        btn.classList.add("copied");
        setTimeout(function(){
          btn.innerHTML = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg> Sao chép';
          btn.classList.remove("copied");
        }, 2000);
      });
    };
  });
}

function renderMarkdown(raw){
  if(!raw) return "";
  var out = raw;

  // Code blocks: ```lang ... ```
  out = out.replace(/```([a-zA-Z0-9_+-]*)\n([\s\S]*?)```/g, function(_, lang, code){
    var l = (lang || "code").toLowerCase();
    return '<div class="code-wrap">' +
      '<div class="code-bar"><span class="code-lang">' + esc(l) + '</span>' +
      '<button type="button" class="copy-code-btn"><svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg> Sao chép</button></div>' +
      '<pre><code>' + esc(code.replace(/\n$/, "")) + '</code></pre></div>';
  });

  // Inline code: `...`
  out = out.replace(/`([^`]+)`/g, function(_, c){ return '<code class="inline">' + esc(c) + '</code>'; });

  // Headers: ###, ##, #
  out = out.replace(/^### (.*$)/gim, '<h3 style="font-size:15px;font-weight:700;margin:12px 0 6px">$1</h3>');
  out = out.replace(/^## (.*$)/gim, '<h2 style="font-size:16.5px;font-weight:700;margin:14px 0 6px">$1</h2>');
  out = out.replace(/^# (.*$)/gim, '<h1 style="font-size:18px;font-weight:700;margin:16px 0 8px">$1</h1>');

  // Bold & Italic
  out = out.replace(/\*\*([^\*]+)\*\*/g, '<b>$1</b>');
  out = out.replace(/\*([^\*]+)\*/g, '<i>$1</i>');

  // Blockquote: > ...
  out = out.replace(/^> (.*$)/gim, '<blockquote style="border-left:3px solid var(--accent);padding-left:12px;margin:8px 0;color:var(--ink-2)">$1</blockquote>');

  // Paragraphs & Line breaks
  var paras = out.split(/\n\n+/);
  return paras.map(function(p){
    p = p.trim();
    if(p.startsWith("<div class=\"code-wrap\"") || p.startsWith("<h") || p.startsWith("<blockquote")) return p;
    return "<p>" + p.replace(/\n/g, "<br>") + "</p>";
  }).join("");
}

function createAiMsgContainer(){
  var row = document.createElement("div");
  row.className = "msg ai";
  row.innerHTML =
    '<div class="ai-head">' + MASCOT_HTML +
      '<span class="ai-name">Mart AI</span><span class="ai-pill">' + currentModelShort() + '</span>' +
    '</div>' +
    '<div class="bubble"></div>' +
    '<div class="ai-meta"><span class="ai-lbl">Mart AI</span><span class="ai-dot"></span><span class="ai-time">' + nowTime() + '</span></div>';
  return row;
}

function createLiveAiBubble(){
  var row = createAiMsgContainer();
  var bubble = row.querySelector(".bubble");
  bubble.innerHTML = '<span class="live-cursor">▍</span>';
  messagesEl.appendChild(row);
  return row;
}

function updateLiveAiBubble(rowEl, fullText){
  if(!rowEl) return;
  var bubble = rowEl.querySelector(".bubble");
  if(!bubble) return;
  bubble.innerHTML = renderMarkdown(fullText) + '<span class="live-cursor">▍</span>';
  wireCodeCopy(bubble);
}

function finalizeAiBubble(rowEl, fullText, tokensUsed){
  if(!rowEl) return;
  var bubble = rowEl.querySelector(".bubble");
  if(bubble){
    bubble.innerHTML = renderMarkdown(fullText);
    wireCodeCopy(bubble);
  }
  var meta = rowEl.querySelector(".ai-meta");
  if(meta){
    meta.innerHTML = '<span class="ai-lbl">Mart AI</span><span class="ai-dot"></span><span class="ai-time">'+nowTime()+'</span>' +
      (tokensUsed ? '<span class="ai-dot"></span><span style="font-size:11.5px;color:var(--ink-2)">' + tokensUsed + ' tok</span>' : '');
    meta.appendChild(actionsBar(function(){ return fullText; }, true));
  }
}

function addAiError(msg){
  var row = createAiMsgContainer();
  var bubble = row.querySelector(".bubble");
  bubble.innerHTML = '<div style="color:var(--err,#e53e3e);background:rgba(229,62,62,0.08);padding:10px 14px;border-radius:10px;font-weight:600;display:flex;align-items:center;gap:8px">' +
    '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>' +
    '<span>' + esc(msg) + '</span>' +
    '</div>';
  messagesEl.appendChild(row);
  scrollDown();
}

var isSending = false;
var queuedPrompts = [];
var CHAT_REQUEST_TIMEOUT_MS = 100000;

/* Đảm bảo token JWT RS256 luôn hợp lệ trước mọi thao tác */
async function ensureValidToken(signal){
  var tok = localStorage.getItem("mart_jwt_token") || "";
  if(!tok){
    try {
      var r = await fetch("/api/auth/session", { cache: "no-store", signal: signal });
      if(r.ok){
        var d = await r.json();
        if(d && d.token){
          tok = d.token;
          localStorage.setItem("mart_jwt_token", tok);
        }
      }
    } catch(e){}
  }
  return tok;
}

/* Hàm fetch bọc xác thực tự động kèm Bearer token và X-Mart-Client */
async function authedFetch(url, opts){
  opts = opts || {};
  opts.headers = opts.headers || {};
  var tok = await ensureValidToken();
  if(tok && !opts.headers["Authorization"]) {
    opts.headers["Authorization"] = "Bearer " + tok;
  }
  if(!opts.headers["X-Mart-Client"]){
    opts.headers["X-Mart-Client"] = "web";
  }
  return fetch(url, opts);
}

/* Gửi tin nhắn thực tế qua SSE Streaming tới /api/chat/send của Mart AI backend */
async function handleSend(text){
  if(!text || !text.trim()) return;
  text = text.trim();
  if(isSending){
    queuedPrompts.push(text);
    toast("Câu hỏi đã được xếp hàng; Mart AI sẽ gửi sau câu trả lời hiện tại.");
    return;
  }
  isSending = true;
  stopBtn.classList.add("show");
  sendBtn.style.display = "none";
  var controller = new AbortController();
  var activeStream = { cancelled: false, timedOut: false, controller: controller };
  streaming = activeStream;
  var requestTimer = setTimeout(function(){
    activeStream.timedOut = true;
    controller.abort();
  }, CHAT_REQUEST_TIMEOUT_MS);
  startConversation();
  addUserMsg(text);
  lastUserText = text;
  input.value = "";
  autoGrow();

  showTyping();

  var curModelObj = currentModel();
  var modelName = (curModelObj && curModelObj.name) || "Muse";
  var title = text.length > 36 ? text.slice(0, 36) + "…" : text;

  var aiBubble = null;
  var accumulatedDelta = "";
  var receivedAnyChunk = false;

  try {
    var authTok = await ensureValidToken(controller.signal);
    var headers = {
      "Content-Type": "application/json",
      "Accept": "application/json",
      "X-Mart-Client": "web",
      "X-Mart-Ext-Relay": "1"
    };
    if(authTok) headers["Authorization"] = "Bearer " + authTok;

    var reqBody = {
      prompt: text,
      thread_id: activeConvId || "",
      model: modelName,
      client_relay_capable: true,
      skip_browser_relay: false,
      stream: false
    };

    updateTypingProgress(0, "\u0110ang g\u1eedi y\u00eau c\u1ea7u t\u1edbi Mart AI server; ch\u01b0a c\u00f3 x\u00e1c nh\u1eadn t\u1eeb server.");
    var resp = await fetch("/api/chat/send", {
      method: "POST",
      headers: headers,
      body: JSON.stringify(reqBody),
      cache: "no-store",
      signal: controller.signal
    });

    updateTypingProgress(1, "Mart AI server \u0111\u00e3 ph\u1ea3n h\u1ed3i HTTP " + resp.status + ".");
    if(!resp.ok){
      var errData = await resp.json().catch(function(){ return {}; });
      throw new Error(errData.error || ("Lỗi kết nối máy chủ (" + resp.status + ")"));
    }

    var contentType = resp.headers.get("content-type") || "";
    if(contentType.indexOf("text/event-stream") >= 0 && resp.body){
      var reader = resp.body.getReader();
      var decoder = new TextDecoder("utf-8");
      var buffer = "";

      while(true){
        if(streaming && streaming.cancelled){
          try { await reader.cancel(); } catch(e){}
          break;
        }
        var chunk = await reader.read();
        if(chunk.done) break;
        if(streaming && streaming.cancelled){
          try { await reader.cancel(); } catch(e){}
          break;
        }
        buffer += decoder.decode(chunk.value, { stream: true }).replace(/\r\n/g, "\n");
        var parts = buffer.split("\n\n");
        buffer = parts.pop();

        for(var i = 0; i < parts.length; i++){
          var block = parts[i].trim();
          if(!block) continue;
          var evType = "message";
          var evData = "";
          var lines = block.split("\n");
          for(var j = 0; j < lines.length; j++){
            if(lines[j].indexOf("event:") === 0) evType = lines[j].slice(6).trim();
            else if(lines[j].indexOf("data:") === 0) evData = lines[j].slice(5).trim();
          }

          if(evType === "start"){
            try {
              var sObj = JSON.parse(evData);
              if(sObj.thread_id && !activeConvId){
                activeConvId = sObj.thread_id;
                saveToHistory(title, "Đang phản hồi…");
              }
            } catch(e){}
          } else if(evType === "delta"){
            try {
              var dObj = JSON.parse(evData);
              var dText = dObj.delta || "";
              var fullText = dObj.content || "";
              if(fullText) accumulatedDelta = fullText;
              else accumulatedDelta += dText;

              if(!receivedAnyChunk && accumulatedDelta.trim()){
                receivedAnyChunk = true;
                hideTyping();
                aiBubble = createLiveAiBubble();
              }
              if(aiBubble){
                updateLiveAiBubble(aiBubble, accumulatedDelta);
                scrollDown();
              }
            } catch(e){}
          } else if(evType === "done"){
            try {
              var doneObj = JSON.parse(evData);
              var finalReply = doneObj.reply || doneObj.content || accumulatedDelta;
              var tokens = doneObj.tokens_used || countTokens(finalReply);
              if(doneObj.thread_id) activeConvId = doneObj.thread_id;

              if(!aiBubble){
                hideTyping();
                aiBubble = createLiveAiBubble();
              }
              finalizeAiBubble(aiBubble, finalReply, tokens);
              hideTyping();
              beep();
              msgCount += 2;
              tokenEst += (tokens || countTokens(text) + countTokens(finalReply));
              updateUsage();
              addActivity(title, "Hoàn thành câu trả lời");
              saveToHistory(title, finalReply.slice(0, 60));
              fetchThreads();
            } catch(e){}
          } else if(evType === "error"){
            var eMsg = "Lỗi khi xử lý phản hồi";
            try {
              var eObj = JSON.parse(evData);
              eMsg = eObj.error || eMsg;
            } catch(e){ eMsg = evData || eMsg; }
            throw new Error(eMsg);
          }
        }
      }
    } else {
      var dataJson = await resp.json();
      if(dataJson && dataJson.browser_relay){
        updateTypingProgress(2, "Server \u0111\u00e3 nh\u1eadn y\u00eau c\u1ea7u; \u0111ang g\u1eedi c\u00e2u h\u1ecfi t\u1edbi Muse.ai qua Chrome.");
        var relayOut;
        try {
          var extension = await requestChromeExtension({ action: "ping" }, 5000);
          if(!extension || !extension.ok || extension.supportsChatRelay === false){
            throw new Error("Chrome extension chua ket noi; hay bat tien ich, dang nhap Muse.ai va tai lai trang.");
          }
          relayOut = await requestChromeExtension({
            action: "execute_muse_chat",
            prompt: dataJson.prompt || text,
            cookie: dataJson.relay_cookie || ""
          }, 90000);
          if(!relayOut || !relayOut.ok || !String(relayOut.content || "").trim()){
            throw new Error((relayOut && relayOut.error) || "Chrome chua nhan duoc cau tra loi moi tu Muse.ai.");
          }
        } catch(relayErr) {
          var relayError = (relayErr && relayErr.message) || "Khong nhan duoc phan hoi moi tu Muse.ai.";
          await authedFetch("/api/chat/relay-complete", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ thread_id: dataJson.thread_id, turn_id: dataJson.turn_id, node_id: dataJson.node_id, model: dataJson.model, error: relayError })
          }).catch(function(){});
          throw new Error(relayError);
        }
        updateTypingProgress(2, "Muse.ai \u0111\u00e3 tr\u1ea3 l\u1eddi; Mart AI \u0111ang l\u01b0u k\u1ebft qu\u1ea3.");
        var completeRes = await authedFetch("/api/chat/relay-complete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            thread_id: dataJson.thread_id,
            turn_id: dataJson.turn_id,
            node_id: dataJson.node_id,
            model: dataJson.model || modelName,
            reply: relayOut.content,
            tokens_used: relayOut.tokens_used || countTokens(relayOut.content),
            latency_ms: relayOut.latency_ms || 0
          })
        });
        var completed = await completeRes.json().catch(function(){ return {}; });
        if(!completeRes.ok || !String(completed.reply || "").trim()){
          throw new Error(completed.error || "Server chua luu duoc cau tra loi nhan tu Chrome.");
        }
        dataJson = completed;
      }
      updateTypingProgress(2, "Mart AI \u0111ang hi\u1ec3n th\u1ecb ph\u1ea3n h\u1ed3i t\u1eeb m\u00e1y ch\u1ee7.");
      hideTyping();
      var reply = dataJson.reply || dataJson.content || "";
      if(!reply) throw new Error("Máy chủ trả về phản hồi rỗng; Mart AI chưa nhận được câu trả lời.");
      if(dataJson.thread_id) activeConvId = dataJson.thread_id;
      aiBubble = createLiveAiBubble();
      finalizeAiBubble(aiBubble, reply, dataJson.tokens_used);
      beep();
      msgCount += 2;
      tokenEst += (dataJson.tokens_used || countTokens(text) + countTokens(reply));
      updateUsage();
      addActivity(title, "Hoàn thành câu trả lời");
      saveToHistory(title, reply.slice(0, 60));
      fetchThreads();
    }
  } catch(err){
    hideTyping();
    if(activeStream.timedOut){
      addAiError("Muse AI chưa trả lời trong 100 giây. Tác vụ đã được dừng; bạn có thể gửi lại hoặc tiếp tục hỏi.");
      toast("Đã dừng yêu cầu quá thời gian chờ.");
      return;
    }
    if(activeStream.cancelled) return;
    addAiError(err.message || "Không thể gửi tin nhắn tới Mart AI");
    toast(err.message || "Không thể gửi tin nhắn");
  } finally {
    clearTimeout(requestTimer);
    isSending = false;
    streaming = null;
    stopBtn.classList.remove("show");
    sendBtn.style.display = "";
    if(queuedPrompts.length){
      var nextPrompt = queuedPrompts.shift();
      setTimeout(function(){ handleSend(nextPrompt); }, 0);
    }
  }
}

/* Lấy danh sách đoạn chat thật từ Backend */
async function fetchThreads(){
  try {
    var res = await authedFetch("/api/threads", { cache: "no-store" });
    if(!res.ok) return;
    var data = await res.json();
    var list = data.threads || [];
    var now = new Date();
    CONVERSATIONS = list.map(function(t){
      var d = new Date(t.created_at || t.updated_at || Date.now());
      var isToday = d.toDateString() === now.toDateString();
      var isYesterday = new Date(now.getTime() - 86400000).toDateString() === d.toDateString();
      var grp = isToday ? "Hôm nay" : (isYesterday ? "Hôm qua" : "Cũ hơn");
      var timeStr = ("0"+d.getHours()).slice(-2)+":"+("0"+d.getMinutes()).slice(-2);
      return {
        id: t.id,
        group: grp,
        title: t.title || "Đoạn chat",
        preview: t.last_preview || t.preview || "",
        time: timeStr,
        messages: Array.isArray(t.messages) ? t.messages : []
      };
    });
    renderHistory($("#searchHist") ? $("#searchHist").value : "");
  } catch(e){}
}

/* Tải tin nhắn thật của thread khi bấm chọn */
async function openConversation(c){
  var threadId = typeof c === "string" ? c : (c ? c.id : null);
  if(!threadId) return;
  activeConvId = threadId;
  closeDrawers();
  startConversation();
  messagesEl.innerHTML = "";
  renderHistory($("#searchHist") ? $("#searchHist").value : "");
  setTitle("Đang tải...");

  try {
    var thread = typeof c === "object" && c ? c : CONVERSATIONS.find(function(item){ return item.id === threadId; });
    if(!thread || !Array.isArray(thread.messages)){
      await fetchThreads();
      thread = CONVERSATIONS.find(function(item){ return item.id === threadId; });
    }
    if(!thread) throw new Error("ERR_THREAD_NOT_FOUND");
    setTitle(thread.title || "Đoạn chat");
    var msgs = thread.messages || [];
    messagesEl.innerHTML = "";
    msgCount = 0;
    tokenEst = 0;
    msgs.forEach(function(m){
      var content = String(m.content || "");
      if(m.role === "assistant" && !content.trim()) return;
      if(m.role === "user"){
        addUserMsg(content);
        tokenEst += countTokens(content);
      } else if(m.role === "assistant") {
        var aiWrap = createAiMsgContainer();
        var bubble = aiWrap.querySelector(".bubble");
        bubble.innerHTML = renderMarkdown(content);
        wireCodeCopy(bubble);
        var meta = aiWrap.querySelector(".ai-meta");
        if(meta){
          meta.innerHTML = '<span class="ai-lbl">Mart AI</span><span class="ai-dot"></span><span class="ai-time">'+esc(m.timestamp || nowTime())+'</span>';
          meta.appendChild(actionsBar(function(){ return content; }, false));
        }
        messagesEl.appendChild(aiWrap);
        tokenEst += countTokens(content);
      }
      msgCount++;
    });
    updateUsage();
    scrollDown();
  } catch(err){
    setTitle("Đoạn chat");
    toast("Không tải được nội dung đoạn chat từ máy chủ");
  }
}

/* Xoá đoạn chat thật */
var histWrapEl = $("#history");
if(histWrapEl){
  histWrapEl.addEventListener("click", async function(e){
    var b = e.target.closest(".hi-del");
    if(!b) return;
    e.stopPropagation();
    var id = b.getAttribute("data-del");
    if(!id) return;
    try {
      await authedFetch("/api/threads/" + encodeURIComponent(id), { method: "DELETE" });
      CONVERSATIONS = CONVERSATIONS.filter(function(x){ return x.id !== id; });
      if(activeConvId === id) resetChat();
      renderHistory($("#searchHist") ? $("#searchHist").value : "");
      toast("Đã xoá đoạn chat");
    } catch(err) {
      toast("Không thể xoá đoạn chat");
    }
  });
}

/* New chat / reset */
function resetChat(){
  hideTyping();
  document.body.classList.remove("is-thinking");
  document.body.classList.remove("is-working");
  messagesEl.innerHTML = "";
  started = false;
  activeConvId = null;
  welcomeEl.style.display = "";
  setTitle("Đoạn chat");
  msgCount = 0; tokenEst = 0; updateUsage();
  renderHistory($("#searchHist").value);
  scrollEl.scrollTop = 0;
}
function newChat(){ resetChat(); closeDrawers(); input.focus(); }
$("#btnNew").addEventListener("click", newChat);
document.addEventListener("keydown", function(e){
  if((e.ctrlKey || e.metaKey) && e.key.toLowerCase()==="k"){ e.preventDefault(); newChat(); }
});


async function fetchPoolReal(){
  try {
    var res = await authedFetch("/api/router/summary", { cache: "no-store" });
    if(!res.ok) throw new Error("Khong tai duoc danh sach Pool (HTTP " + res.status + ")");
    var data = await res.json();
    var nodes = Array.isArray(data.nodes) ? data.nodes : [];
    if(typeof data.routing_strategy === "string" && STRAT_NAMES[data.routing_strategy]){
      strategy = pendingStrategy = data.routing_strategy;
      $$("#stratGrid .strat-card").forEach(function(card){ card.classList.toggle("on", card.getAttribute("data-strat") === strategy); });
    }
    if(typeof data.auto_failover === "boolean"){
      failover = pendingFailover = data.auto_failover;
      $("#swFailover").classList.toggle("on", failover);
    }
    $("#routerCurrent").textContent = (STRAT_NAMES[strategy] || strategy) + " - Auto-Failover " + (failover ? "enabled" : "disabled");
    $("#poolMiniStrat").textContent = STRAT_NAMES[strategy] || strategy;
    $("#poolMiniFail").textContent = failover ? "Enabled" : "Disabled";
    POOL = nodes.map(function(n){
      return {
        id: n.id,
        label: n.label || n.id || "Muse",
        plan: n.plan_tier || n.free_plan_label || "Muse.ai",
        status: n.status || "unavailable",
        auth: n.auth_type === "api_key" ? "API Key" : "Cookie",
        fp: n.fingerprint || n.id || "",
        tokens: Number(n.tokens_remaining) || 0,
        freeVal: n.free_plan_value || "Chưa lấy được dữ liệu mức sử dụng",
        topupVal: n.topup_value || "Chưa xác minh số dư từ Muse.ai",
        freePct: Math.max(0, Math.min(100, Number(n.free_plan_percent) || 0)),
        topPct: Math.max(0, Math.min(100, Number(n.topup_percent) || 0)),
        req: Number(n.total_requests) || 0,
        fail: Number(n.fail_count) || 0,
        lat: Number(n.latency_ms) || 0,
        dev: "Không thu thập",
        proxy: n.proxy_url ? "Da cau hinh" : "Truc tiep",
        detail: n.status_detail || (n.status === "ready" ? "Phien da xac thuc" : "Chua xac minh duoc phien")
      };
    });
    renderPool();
  } catch(e){
    toast(e.message || "Khong tai duoc Pool tu may chu");
  }
}

/* Mode segmented control */
var MODE_HINTS = {
  chat: "Trả lời tự nhiên, cân bằng tốc độ và chi tiết.",
  agent: "Chia nhiệm vụ thành từng bước, báo rõ tiến độ.",
  code: "Ưu tiên code chạy được, khối code riêng, kèm giải thích ngắn."
};
var MODE_PLACEHOLDERS = {
  chat: "Nhắn tin cho Mart AI…",
  agent: "Mô tả nhiệm vụ — Mart sẽ chia bước và thực thi…",
  code: "Mô tả hàm, lỗi hoặc đoạn code cần viết…"
};
$$("#modeSeg button").forEach(function(b){
  b.addEventListener("click", function(){
    mode = b.getAttribute("data-mode");
    $$("#modeSeg button").forEach(function(x){ x.classList.toggle("on", x===b); });
    $("#modeHint").textContent = MODE_HINTS[mode];
    $$(".set-info-mode").forEach(function(el){ el.textContent = modeLabel(); });
    input.placeholder = MODE_PLACEHOLDERS[mode];
    toast("Đã chuyển sang chế độ " + modeLabel());
    input.focus();
  });
});

/* Composer */
function autoGrow(){
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight, 132) + "px";
  sendBtn.disabled = input.value.trim().length===0;
  charCount.textContent = input.value.length + " / 2000";
}
input.addEventListener("input", function(){
  if(input.value.length > 2000) input.value = input.value.slice(0,2000);
  autoGrow();
});
input.addEventListener("keydown", function(e){
  if(e.key==="Enter" && !e.shiftKey){ e.preventDefault(); $("#composerForm").requestSubmit(); }
});
$("#composerForm").addEventListener("submit", function(e){
  e.preventDefault();
  var text = input.value.trim(); if(!text) return;
  input.value = ""; autoGrow();
  handleSend(text);
});

/* Attach menu */
var attachMenu = $("#attachMenu");
$("#attachBtn").addEventListener("click", function(e){ e.stopPropagation(); attachMenu.classList.toggle("open"); });
document.addEventListener("click", function(e){ if(!attachMenu.contains(e.target)) attachMenu.classList.remove("open"); });
$$("[data-attach]").forEach(function(b){
  b.addEventListener("click", function(){
    attachMenu.classList.remove("open");
    toast("“" + b.getAttribute("data-attach") + "” — tính năng chạy đầy đủ ở bản Go chính thức");
  });
});

/* Voice (demo) */
var recTimer = null;
function stopRec(fill){
  clearTimeout(recTimer);
  $("#recBar").classList.remove("show");
  $("#micBtn").classList.remove("rec");
  if(fill){
    input.value = "Giải thích closure trong JavaScript giúp mình";
    autoGrow(); input.focus();
    toast("Đã chuyển giọng nói thành văn bản (demo)");
  }
}
$("#micBtn").addEventListener("click", function(){
  $("#recBar").classList.add("show");
  $("#micBtn").classList.add("rec");
  recTimer = setTimeout(function(){ stopRec(true); }, 2600);
});
$("#recDone").addEventListener("click", function(){ stopRec(true); });

/* Suggestion cards & chips & caps */
$$(".sugg").forEach(function(b){ b.addEventListener("click", function(){ runFlow(b.getAttribute("data-flow")); }); });
$$("#welcome .chip-q").forEach(function(b){ b.addEventListener("click", function(){ handleSend(b.getAttribute("data-ask")); }); });
$$(".cap").forEach(function(b){
  b.addEventListener("click", function(){
    if(b.getAttribute("data-flow")) runFlow(b.getAttribute("data-flow"));
    else if(b.getAttribute("data-ask")){ handleSend(b.getAttribute("data-ask")); closeDrawers(); }
  });
});

/* Drawers */
var sidebar = $("#sidebar"), scrim = $("#scrim");
function closeDrawers(){ sidebar.classList.remove("open"); scrim.classList.remove("show"); }
$("#openSidebar").addEventListener("click", function(){ sidebar.classList.add("open"); scrim.classList.add("show"); });
$("#closeSidebar").addEventListener("click", closeDrawers);
scrim.addEventListener("click", closeDrawers);

/* Session actions */
$("#exportBtn").addEventListener("click", function(){
  var lines = ["Mart AI — Bản ghi cuộc trò chuyện", "Xuất lúc: " + nowTime() + " · Chế độ: " + modeLabel(), "----------------------------------------", ""];
  $$("#messages .msg").forEach(function(mEl){
    var isUser = mEl.classList.contains("user");
    var txt = mEl.querySelector(".bubble") ? mEl.querySelector(".bubble").textContent.trim() : "";
    if(txt) lines.push((isUser ? "Bạn: " : "Mart AI: ") + txt, "");
  });
  if(lines.length <= 4) lines.push("(Chưa có tin nhắn nào trong phiên này.)");
  var blob = new Blob([lines.join("\n")], { type:"text/plain;charset=utf-8" });
  var a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "mart-ai-phien-tro-chuyen.txt";
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(function(){ URL.revokeObjectURL(a.href); }, 2000);
  toast("Đã xuất bản ghi cuộc trò chuyện");
});
$("#clearSessionBtn").addEventListener("click", function(){
  resetChat();
  activityItems = []; renderActivity();
  $("#ctxNote").textContent = "Chưa có ngữ cảnh nào. Khi bạn trò chuyện, Mart sẽ ghi nhớ chủ đề trong phiên này tại đây.";
  toast("Đã xoá phiên làm việc hiện tại");
  closeModal();
  closeDrawers();
});

/* Panel controls */
function syncSwitches(){
  $("#swStreamPanel").classList.toggle("on", settings.stream);
}
$("#swDeep").addEventListener("click", function(){
  this.classList.toggle("on"); deepThink = this.classList.contains("on");
  toast(deepThink ? "Đã bật suy luận sâu" : "Đã tắt suy luận sâu");
});
$("#verbositySel").addEventListener("change", function(){
  verbosity = this.value;
  toast("Độ chi tiết: " + verbosityLabel());
});
$("#swStreamPanel").addEventListener("click", function(){ settings.stream = !settings.stream; syncSwitches(); });

/* Settings modal & theme */
var modal = $("#settingsModal");
function openModal(){ modal.classList.add("open"); }
function closeModal(){ modal.classList.remove("open"); }
$("#footSettings").addEventListener("click", openModal);

$$("[data-close-modal]").forEach(function(el){ el.addEventListener("click", closeModal); });
$("#footAdmin").addEventListener("click", function(){ openAdmin(); closeDrawers(); });
function setTheme(val){
  var root = document.documentElement;
  if(val==="auto") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", val);
  $$("#themeSeg button").forEach(function(b){ b.classList.toggle("on", b.getAttribute("data-theme-val")===val); });
}
$$("#themeSeg button").forEach(function(b){ b.addEventListener("click", function(){ setTheme(b.getAttribute("data-theme-val")); }); });
$("#themeBtn").addEventListener("click", function(){
  var cur = document.documentElement.getAttribute("data-theme");
  var isDark = cur==="dark" || (!cur && window.matchMedia("(prefers-color-scheme: dark)").matches);
  setTheme(isDark ? "light" : "dark");
  toast(isDark ? "Đã chuyển sang giao diện sáng" : "Đã chuyển sang giao diện tối");
});
function wireSwitch(id, fn){
  var el = $(id);
  el.addEventListener("click", function(){ el.classList.toggle("on"); fn(el.classList.contains("on")); });
}
wireSwitch("#swSound", function(on){ settings.sound = on; if(on) setTimeout(beep, 60); });
wireSwitch("#swFollow", function(on){ settings.follow = on; });
$("#clearAllBtn").addEventListener("click", function(){
  CONVERSATIONS = [];
  activeConvId = null;
  renderHistory("");
  resetChat();
  closeModal();
  toast("Đã xoá toàn bộ lịch sử chat");
});
document.addEventListener("keydown", function(e){ if(e.key==="Escape"){ closeModal(); closeDrawers(); attachMenu.classList.remove("open"); } });


/* Model picker and account state come from the server. */
var MODELS = [];
var DEFAULT_MODEL = { id:"muse", name:"Muse", short:"Muse" };
var currentModelId = "muse";
function currentModel(){
  for(var i=0;i<MODELS.length;i++) if(MODELS[i].id===currentModelId) return MODELS[i];
  return DEFAULT_MODEL;
}
var scanState = "idle", scanTimeStr = "";
function nodeStatusLabel(st){ return st==="ready" ? "Phiên hợp lệ" : (st==="running" ? "Đang chạy" : "Không khả dụng"); }
function nodeStatusCls(st){ return st==="ready" ? "st-ready" : (st==="running" ? "st-running" : "st-bad"); }
function renderModelMenu(){
  var list = $("#modelList"), status = $("#scanStatus"), btn = $("#rescanModels");
  if(!list) return;
  if(scanState === "scanning"){
    status.textContent = "Đang kiểm tra Pool tài khoản Muse...";
    btn.disabled = true;
    list.innerHTML = '<div class="scan-loading"><span class="spinner"></span>Đang kiểm tra trạng thái tài khoản trên máy chủ.</div>';
    return;
  }
  btn.disabled = false;
  if(scanState === "error"){
    status.textContent = scanTimeStr || "Không đọc được Pool từ máy chủ";
    list.innerHTML = '<div class="scan-loading">Không thể tải trạng thái tài khoản. Hãy thử lại.</div>';
    return;
  }
  if(scanState !== "done"){
    status.textContent = "Chưa kiểm tra Pool";
    list.innerHTML = '<div class="scan-loading">Danh sách sẽ hiện sau khi đọc trạng thái từ máy chủ.</div>';
    return;
  }
  var avail = MODELS.filter(function(m){ return m.status !== "unavailable"; }).length;
  status.textContent = "Muse Pool: " + avail + " phiên hợp lệ";
  list.innerHTML = "";
  if(!MODELS.length){ list.innerHTML = '<div class="scan-loading">Máy chủ không trả về tài khoản Muse khả dụng.</div>'; return; }
  MODELS.forEach(function(m){
    var b = document.createElement("button");
    b.type = "button";
    b.className = "model-opt" + (m.id === currentModelId ? " on" : "");
    b.setAttribute("data-model", m.id);
    if(m.status === "unavailable") b.disabled = true;
    b.innerHTML = '<span><span class="mo-t">' + esc(m.name) + '</span>' +
      '<span class="mo-meta"><span class="mo-src">' + esc(m.node) + ' · ' + esc(m.plan) + ' · ' + esc(m.auth) + '</span><span class="chip-mini ' + nodeStatusCls(m.status) + '">' + nodeStatusLabel(m.status) + '</span></span>' +
      '<span class="mo-d">' + esc(m.desc) + '</span></span>' +
      '<svg class="mo-check" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>';
    list.appendChild(b);
  });
}
async function scanModels(){
  if(scanState==="scanning") return;
  scanState = "scanning";
  renderModelMenu();
  try {
    var res = await authedFetch("/api/router/summary", { cache: "no-store" });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok) throw new Error(data.error || "Pool API unavailable");
    var nodes = Array.isArray(data.nodes) ? data.nodes : [];
    var readyCount = Math.max(Number(data.ready_nodes) || 0, nodes.filter(function(n){ return n.status === "ready" || n.status === "running"; }).length);
    MODELS = readyCount ? [{ id:"muse", name:"Muse", short:"Muse", desc:"Phiên Muse đã xác thực; chưa xác minh khả năng chat.", node:readyCount + " phiên hợp lệ", plan:"Muse.ai", auth:"Pool", status:"ready" }] : [];
    currentModelId = "muse";
    scanState = "done";
    scanTimeStr = nowTime();
  } catch(err) {
    MODELS = [];
    scanState = "error";
    scanTimeStr = err.message || "API error";
  }
  renderModelMenu();
}
function currentModelShort(){ return currentModel().short; }
function setModeUI(m){
  mode = m;
  $$("#modeSeg button").forEach(function(x){ x.classList.toggle("on", x.getAttribute("data-mode")===m); });
  $("#modeHint").textContent = MODE_HINTS[m];
  $$(".set-info-mode").forEach(function(el){ el.textContent = modeLabel(); });
  input.placeholder = MODE_PLACEHOLDERS[m];
}
function selectModel(id, silent){
  currentModelId = id;
  var m = currentModel();
  $("#modelNameTop").textContent = m.short;
  $$(".set-info-model").forEach(function(el){ el.textContent = m.name; });
  $("#welcomeModel").textContent = m.short;
  $$(".model-opt").forEach(function(o){ o.classList.toggle("on", o.getAttribute("data-model")===id); });
  if(!silent) toast("Đã chọn model " + m.name + " cho đoạn chat này");
}
var modelMenu = $("#modelMenu");
$("#modelBtn").addEventListener("click", function(e){
  e.stopPropagation();
  modelMenu.classList.toggle("open");
  if(modelMenu.classList.contains("open") && scanState==="idle") scanModels();
});
document.addEventListener("click", function(e){ if(!modelMenu.contains(e.target)) modelMenu.classList.remove("open"); });
$("#rescanModels").addEventListener("click", function(e){ e.stopPropagation(); scanModels(); });
$("#modelList").addEventListener("click", function(e){
  var o = e.target.closest(".model-opt");
  if(!o || o.disabled) return;
  selectModel(o.getAttribute("data-model"), false);
  modelMenu.classList.remove("open");
  input.focus();
});

/* Admin view open/close & Khôi phục quyền Quản trị viên */
async function restoreAdminLogin(){
  var pw = prompt("Nhập mật khẩu tài khoản quản trị:", "");
  if(!pw) return;
  try {
    var res = await fetch("/api/users/login", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Mart-Client": "web" },
      body: JSON.stringify({ username: "admin", password: pw })
    });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok){ toast(data.error || "Không xác thực được tài khoản quản trị"); return; }
    if(!data.token || !data.user || data.user.role !== "admin"){
      toast("Máy chủ không trả về phiên quản trị hợp lệ");
      return;
    }
    localStorage.setItem("mart_jwt_token", data.token);
    localStorage.setItem("mart_user", JSON.stringify(data.user));
    setRoleView("admin", data.user);
    await fetchUsersReal();
    await fetchThreads();
    document.body.classList.add("admin-open");
    $("#adminView").scrollTop = 0;
    closeDrawers();
    toast("Đã xác thực quyền quản trị: " + (data.user.display_name || data.user.username));
  } catch(e){
    toast("Không thể kết nối máy chủ để xác thực");
  }
}

function openAdmin(){
  if(document.body.classList.contains("user-view")){
    restoreAdminLogin();
    return;
  }
  document.body.classList.add("admin-open");
  $("#adminView").scrollTop = 0;
  closeDrawers();
  fetchUsersReal();
  fetchPoolReal();
}
function closeAdmin(){ document.body.classList.remove("admin-open"); }
$("#btnExitAdmin").addEventListener("click", closeAdmin);
$("#openAdminFromPanel").addEventListener("click", function(){ closeModal(); openAdmin(); });
$("#setOpenAdmin").addEventListener("click", function(){ closeModal(); openAdmin(); });

/* Gắn sự kiện click vào khối persona / avatar ở góc dưới bên trái */
var userCardEl = $(".user-card");
if(userCardEl){
  userCardEl.style.cursor = "pointer";
  userCardEl.title = "Bấm để mở Quản trị viên / Khôi phục quyền Admin";
  userCardEl.addEventListener("click", function(e){
    if(e.target.closest("#themeBtn")) return;
    openAdmin();
  });
}

/* Pool tài khoản Muse.ai riêng (Admin) — đúng cấu trúc MuseAccountNode */
var POOL = [];
function fmtTok(n){
  if(n>=1000000) return "≈" + (Math.round(n/100000)/10).toString().replace(".",",") + "M";
  if(n>=1000) return "≈" + Math.round(n/1000) + "K";
  return String(n);
}
function nodeStatusChip(st){
  if(st==="ready") return '<span class="chip-mini st-ready">Phiên hợp lệ</span>';
  if(st==="running") return '<span class="chip-mini st-running">Đang chạy</span>';
  if(st==="checking") return '<span class="chip-mini st-running">Đang kiểm tra…</span>';
  return '<span class="chip-mini st-bad">Không khả dụng</span>';
}
function renderPool(){
  var wrap = $("#poolList"); wrap.innerHTML = "";
  POOL.forEach(function(n, idx){
    var card = document.createElement("div");
    card.className = "node-card";
    card.innerHTML =
      '<div class="node-top"><span class="node-name">' + esc(n.label) + '</span>' +
      '<span class="chip-mini">' + esc(n.plan) + '</span><span class="chip-mini">' + esc(n.auth) + '</span>' + nodeStatusChip(n.status) +
      '<span class="node-actions"><button type="button" data-nact="sync" data-ni="' + idx + '">Đồng bộ</button>' +
      '<button type="button" data-nact="del" data-ni="' + idx + '">Xoá</button></span></div>' +
      '<div class="meter"><div class="m-row"><span class="mk">Gói miễn phí</span><span class="mv">' + esc(n.freeVal) + '</span></div><div class="bar"><i style="width:' + n.freePct + '%"></i></div></div>' +
      '<div class="meter"><div class="m-row"><span class="mk">Token bổ sung (Muse.ai báo)</span><span class="mv">' + esc(n.topupVal) + '</span></div><div class="bar"><i style="width:' + n.topPct + '%"></i></div></div>' +
      '<div class="node-meta"><span>Lượt gọi <b>' + n.req + '</b></span><span>Lỗi <b>' + n.fail + '</b></span><span>Độ trễ <b>' + (n.lat ? n.lat + "ms" : "—") + '</b></span><span>Thiết bị <b>' + esc(n.dev) + '</b></span><span>Proxy <b>' + esc(n.proxy) + '</b></span></div>' +
      '<div class="node-detail">' + esc(n.detail) + '</div>';
    wrap.appendChild(card);
  });
  var ready = POOL.filter(function(n){ return n.status==="ready"; }).length;
  var running = POOL.filter(function(n){ return n.status==="running"; }).length;
  var bad = POOL.filter(function(n){ return n.status==="unavailable"; }).length;
  var reqs = POOL.reduce(function(a,n){ return a + n.req; }, 0);
  $("#statNodes").textContent = POOL.length;
  $("#statReady").textContent = ready + " / " + POOL.length;
  $("#statReadySub").textContent = running + " đang chạy · " + bad + " không khả dụng";
  $("#statTokens").textContent = POOL.length === 1 ? POOL[0].topupVal : (POOL.length ? "Xem từng tài khoản" : "Chưa có dữ liệu");
  $("#statReqs").textContent = reqs;
  $("#poolMiniCount").textContent = POOL.length + " node · " + ready + " phiên hợp lệ";
}
$("#poolList").addEventListener("click", async function(e){
  var b = e.target.closest("[data-nact]"); if(!b) return;
  var idx = parseInt(b.getAttribute("data-ni"), 10);
  var n = POOL[idx]; if(!n || !n.id) return;
  var action = b.getAttribute("data-nact");
  if(action === "del" && b.getAttribute("data-armed") !== "1"){
    b.setAttribute("data-armed", "1"); b.classList.add("danger-armed"); b.textContent = "Xoa that?";
    setTimeout(function(){ b.removeAttribute("data-armed"); b.classList.remove("danger-armed"); b.textContent = "Xoa"; }, 2600);
    return;
  }
  b.disabled = true;
  try {
    var path = action === "sync" ? "/api/accounts/wake" : "/api/accounts/delete";
    var result = await authedFetch(path, {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify(action === "sync" ? { node_id: n.id } : { node_id: n.id })
    });
    var data = await result.json().catch(function(){ return {}; });
    if(!result.ok) throw new Error(data.error || "Account operation failed (HTTP " + result.status + ")");
    await fetchPoolReal();
    toast(action === "sync" ? (data.detail || "Muse session checked") : ("Removed " + n.label));
  } catch(err) { toast(err.message || "Account operation failed"); }
  finally { b.disabled = false; }
});
$("#btnSyncAll").addEventListener("click", async function(){
  var btn = this; btn.disabled = true;
  try {
    var res = await authedFetch("/api/accounts/wake", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ node_id: "all" })
    });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok) throw new Error(data.error || "Pool sync failed (HTTP " + res.status + ")");
    await fetchPoolReal();
    toast("Checked " + (data.probed || 0) + " Muse account(s)");
  } catch(err) { toast(err.message || "Pool sync failed"); }
  finally { btn.disabled = false; }
});
$("#accAuth").addEventListener("change", function(){
  var field = $("#accUpstreamField");
  if(field) field.style.display = this.value === "api_key" ? "block" : "none";
});
function requestChromeExtension(payload, timeoutMs){
  return new Promise(function(resolve, reject){
    var reqId = "mart-ext-" + Date.now() + "-" + Math.random().toString(36).slice(2);
    var timer = setTimeout(function(){
      window.removeEventListener("message", onMessage);
      reject(new Error("Không nhận được phản hồi từ tiện ích Chrome. Hãy cài/bật Mart AI Extension, sau đó tải lại trang."));
    }, timeoutMs || 5000);
    function onMessage(ev){
      if(ev.source !== window || ev.origin !== window.location.origin || !ev.data || ev.data.__mart_ext_resp !== true || ev.data.reqId !== reqId) return;
      clearTimeout(timer);
      window.removeEventListener("message", onMessage);
      if(ev.data.error) reject(new Error(ev.data.error));
      else resolve(ev.data.result || null);
    }
    window.addEventListener("message", onMessage);
    window.postMessage({ __mart_ext_req: true, reqId: reqId, payload: payload }, window.location.origin);
  });
}
$("#btnCaptureMuseChrome").addEventListener("click", async function(){
  var btn = this;
  var status = $("#chromeCaptureStatus");
  btn.disabled = true;
  status.textContent = "Đang mở Muse.ai trong Chrome; hãy hoàn tất đăng nhập…";
  try {
    var captured = await requestChromeExtension({ action: "login_and_capture" }, 185000);
    if(!captured || !captured.ok || !captured.cookie) throw new Error((captured && captured.error) || "Chrome chưa trả về phiên đăng nhập Muse.ai.");
    status.textContent = "Đã bắt phiên. Đang xác thực và lưu vào Pool…";
    var res = await authedFetch("/api/accounts/add", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        label: $("#accLabel").value.trim(),
        plan_tier: "Muse.ai",
        auth_type: "cookie",
        upstream_url: "https://muse.ai",
        proxy_url: $("#accProxy").value.trim(),
        credential: captured.cookie,
        tokens_remaining: 0
      })
    });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok) throw new Error(data.error || "Không lưu được phiên Muse.ai vào Pool (HTTP " + res.status + ").");
    await fetchPoolReal();
    status.textContent = data.status_detail || "Đã bắt phiên và đồng bộ tài khoản Muse.ai.";
    toast("Đã bắt phiên Chrome và cập nhật Pool.");
  } catch(err){
    status.textContent = err.message || "Không bắt được phiên Chrome.";
    toast(status.textContent);
  } finally {
    btn.disabled = false;
  }
});
$("#addNodeForm").addEventListener("submit", async function(e){
  e.preventDefault();
  var cred = $("#accCred").value.trim();
  if(!cred){ toast("Paste an account credential first"); $("#accCred").focus(); return; }
  var authType = $("#accAuth").value === "api_key" ? "api_key" : "cookie";
  var label = $("#accLabel").value.trim();
  var proxy = $("#accProxy").value.trim();
  var upstream = authType === "api_key" ? $("#accUpstream").value.trim() : "https://muse.ai";
  if(authType === "api_key" && !upstream){ toast("Enter the official API endpoint for this key"); $("#accUpstream").focus(); return; }
  var lines = cred.split(/\r?\n+/).map(function(x){ return x.trim(); }).filter(Boolean);
  var submit = $("#addNodeForm").querySelector('[type="submit"]');
  if(submit) submit.disabled = true;
  try {
    var path = lines.length > 1 ? "/api/accounts/batch" : "/api/accounts/add";
    var body = lines.length > 1
      ? { lines: lines, plan_tier: "Muse.ai", auth_type: authType, upstream_url: upstream, proxy_url: proxy, tokens_per_account: 0 }
      : { label: label, plan_tier: "Muse.ai", auth_type: authType, upstream_url: upstream, proxy_url: proxy, credential: cred, tokens_remaining: 0 };
    var res = await authedFetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok) throw new Error(data.error || "Account was not saved (HTTP " + res.status + ")");
    $("#accCred").value = ""; $("#accLabel").value = "";
    if($("#accUpstream")) $("#accUpstream").value = "";
    await fetchPoolReal();
    if(lines.length > 1) toast("Saved " + (data.added || 0) + "; verified " + (data.verified_ready || 0));
    else toast(data.status_detail || (data.status === "ready" ? "Account verified" : "Account saved; verification failed"));
  } catch(err) { toast(err.message || "Could not add account"); }
  finally { if(submit) submit.disabled = false; }
});

/* Điều phối xoay tài khoản */
var STRAT_NAMES = { least_active:"Ít bận nhất", highest_tokens:"Nhiều token nhất", round_robin:"Xoay vòng tuần tự" };
var strategy = "least_active", pendingStrategy = "least_active", failover = true, pendingFailover = true;
$$("#stratGrid .strat-card").forEach(function(c){
  c.addEventListener("click", function(){
    pendingStrategy = c.getAttribute("data-strat");
    $$("#stratGrid .strat-card").forEach(function(x){ x.classList.toggle("on", x===c); });
  });
});
$("#swFailover").addEventListener("click", function(){
  this.classList.toggle("on"); pendingFailover = this.classList.contains("on");
});
$("#btnSaveRouter").addEventListener("click", async function(){
  var btn = this; btn.disabled = true;
  try {
    var res = await authedFetch("/api/router/config", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ routing_strategy: pendingStrategy, auto_failover: pendingFailover })
    });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok) throw new Error(data.error || "Router settings were not saved");
    strategy = data.routing_strategy || pendingStrategy;
    pendingStrategy = strategy;
    failover = typeof data.auto_failover === "boolean" ? data.auto_failover : pendingFailover;
    pendingFailover = failover;
    $("#poolMiniStrat").textContent = STRAT_NAMES[strategy] || strategy;
    $("#poolMiniFail").textContent = failover ? "Enabled" : "Disabled";
    $("#routerCurrent").textContent = (STRAT_NAMES[strategy] || strategy) + " - Auto-Failover " + (failover ? "enabled" : "disabled");
    toast("Router settings saved on server");
  } catch(err) { toast(err.message || "Router settings could not be saved"); }
  finally { btn.disabled = false; }
});

/* Tài khoản con & người dùng thật kết nối trực tiếp MySQL / TiDB (mart_users) */
var USERS = [];

async function fetchUsersReal(){
  try {
    var res = await authedFetch("/api/admin/users", { cache: "no-store" });
    if(!res.ok) return;
    var data = await res.json();
    var list = data.users || [];
    USERS = list.map(function(u){
      var d = u.last_login_at;
      var lastStr = "Chưa đăng nhập";
      if(d){
        try {
          var dt = new Date(d);
          var now = new Date();
          if(dt.toDateString() === now.toDateString()){
            lastStr = "Hôm nay · " + ("0"+dt.getHours()).slice(-2) + ":" + ("0"+dt.getMinutes()).slice(-2);
          } else {
            lastStr = ("0"+dt.getDate()).slice(-2) + "/" + ("0"+(dt.getMonth()+1)).slice(-2) + "/" + dt.getFullYear();
          }
        } catch(e){ lastStr = String(d); }
      }
      return {
        id: u.id,
        name: u.display_name || u.username,
        un: u.username,
        role: u.role || "user",
        disabled: !!u.is_disabled,
        threads: u.thread_count || 0,
        docs: u.doc_count || 0,
        mem: u.memory_count || 0,
        tasks: u.task_count || 0,
        last: lastStr
      };
    });
    renderUsers();
  } catch(e){}
}

function renderUsers(){
  var wrap = $("#usersList"); if(!wrap) return;
  wrap.innerHTML = "";
  if(USERS.length === 0){
    wrap.innerHTML = '<div style="padding:16px;text-align:center;color:var(--ink-2);font-size:13.5px">Đang tải danh sách tài khoản từ cơ sở dữ liệu…</div>';
    return;
  }
  USERS.forEach(function(u, idx){
    var row = document.createElement("div");
    row.className = "user-row" + (u.disabled ? " off" : "");
    var isAdmin = u.role==="admin";
    row.innerHTML =
      '<span class="u-av">' + esc(u.name.charAt(0).toUpperCase()) + '</span>' +
      '<span class="u-main"><span class="u-name">' + esc(u.name) + '</span> ' +
      (isAdmin ? '<span class="chip-mini st-ready">Quản trị viên</span>' : '<span class="chip-mini">Tài khoản con</span>') + ' ' +
      (u.disabled ? '<span class="chip-mini st-bad">Đã khoá</span>' : '<span class="chip-mini st-ready">Đang hoạt động</span>') +
      '<span class="u-sub" style="display:block">@' + esc(u.un) + ' · Đăng nhập gần nhất: ' + esc(u.last) + (isAdmin ? ' · Toàn quyền Web &amp; Chat' : ' · Không gian riêng độc lập') + '</span>' +
      '<span class="u-counts"><span>Đoạn chat <b>' + u.threads + '</b></span><span>Tài liệu <b>' + u.docs + '</b></span><span>Bộ nhớ <b>' + u.mem + '</b></span><span>Nhiệm vụ <b>' + u.tasks + '</b></span></span></span>' +
      (isAdmin
        ? '<span class="u-actions"><span style="font-size:12.5px;color:var(--ink-2);align-self:center">Tài khoản gốc — không khoá / không xoá được</span></span>'
        : '<span class="u-actions"><button type="button" data-uact="toggle" data-ui="' + idx + '">' + (u.disabled ? "Mở khoá" : "Khoá") + '</button>' +
          '<button type="button" data-uact="reset" data-ui="' + idx + '">Đặt lại mật khẩu</button>' +
          '<button type="button" data-uact="del" data-ui="' + idx + '">Xoá</button></span>');
    wrap.appendChild(row);
  });
}

$("#usersList").addEventListener("click", async function(e){
  var b = e.target.closest("[data-uact]"); if(!b) return;
  var u = USERS[parseInt(b.getAttribute("data-ui"), 10)]; if(!u) return;
  var act = b.getAttribute("data-uact");

  if(act==="toggle"){
    try {
      var res = await authedFetch("/api/admin/users/toggle", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          user_id: u.id || u.un,
          is_disabled: !u.disabled
        })
      });
      if(res.ok){
        u.disabled = !u.disabled;
        renderUsers();
        toast(u.disabled ? ("Đã khoá tài khoản @" + u.un) : ("Đã mở khoá tài khoản @" + u.un));
      } else {
        var errD = await res.json().catch(function(){ return {}; });
        toast(errD.error || "Không thể cập nhật trạng thái");
      }
    } catch(err){
      toast("Lỗi kết nối máy chủ");
    }
  }

  if(act==="reset"){
    var newPw = prompt("Nhập mật khẩu mới cho @" + u.un + " (tối thiểu 6 ký tự):", "");
    if(!newPw) return;
    if(newPw.length < 6){ toast("Mật khẩu phải tối thiểu 6 ký tự"); return; }
    try {
      var res = await authedFetch("/api/admin/users/reset-password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          user_id: u.id || u.un,
          new_password: newPw
        })
      });
      if(res.ok){
        toast("Đã đặt lại mật khẩu cho @" + u.un + " thành công");
      } else {
        var errD = await res.json().catch(function(){ return {}; });
        toast(errD.error || "Không thể đặt lại mật khẩu");
      }
    } catch(err){
      toast("Lỗi kết nối máy chủ");
    }
  }

  if(act==="del"){
    if(b.getAttribute("data-armed")==="1"){
      try {
        var res = await authedFetch("/api/admin/users/delete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ user_id: u.id || u.un })
        });
        if(res.ok){
          toast("Đã xoá @" + u.un + " cùng toàn bộ dữ liệu riêng của tài khoản này");
          await fetchUsersReal();
        } else {
          var errD = await res.json().catch(function(){ return {}; });
          toast(errD.error || "Không thể xoá tài khoản");
        }
      } catch(err){
        toast("Lỗi kết nối máy chủ");
      }
    } else {
      b.setAttribute("data-armed","1"); b.classList.add("danger-armed"); b.textContent = "Xoá thật?";
      setTimeout(function(){
        b.removeAttribute("data-armed");
        b.classList.remove("danger-armed");
        b.textContent = "Xoá";
      }, 2600);
    }
  }
});

$("#createUserForm").addEventListener("submit", async function(e){
  e.preventDefault();
  var un = $("#nuUser").value.trim().toLowerCase();
  var nm = $("#nuName").value.trim();
  var pw = $("#nuPass").value;
  var role = $("#nuRole").value;
  if(!/^[a-z0-9_.-]{3,32}$/.test(un)){ toast("Tên đăng nhập phải 3–32 ký tự: chữ, số, dấu . _ -"); return; }
  if(pw.length < 6){ toast("Mật khẩu phải tối thiểu 6 ký tự"); return; }

  try {
    var res = await authedFetch("/api/users/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        username: un,
        display_name: nm || un,
        password: pw,
        role: role
      })
    });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok){
      throw new Error(data.error || "Không thể tạo tài khoản");
    }
    toast("Đã tạo thành công tài khoản @" + un + " trên cơ sở dữ liệu");
    $("#nuUser").value = ""; $("#nuName").value = ""; $("#nuPass").value = "";
    await fetchUsersReal();
  } catch(err){
    toast(err.message || "Lỗi tạo tài khoản");
  }
});

/* Xem theo quyền: Admin <-> Tài khoản con */
function setRoleView(v, actualUser){
  var user = actualUser || {};
  var isUser = v === "user" || user.role === "user";
  var displayName = user.display_name || user.username || "Tài khoản";
  var username = user.username || "";
  document.body.classList.toggle("user-view", isUser);
  closeAdmin();
  $("#personaAvatar").textContent = displayName.charAt(0).toUpperCase() || "?";
  $("#personaName").textContent = displayName;
  $("#personaStatus").textContent = isUser ? ("Không gian riêng · @" + username) : "Đang hoạt động";
  $("#personaRole").textContent = isUser ? "Tài khoản" : "Admin";
  $("#setPersonaLine").textContent = isUser ? (displayName + " · Tài khoản (@" + username + ")") : (displayName + " · Quản trị viên");
  var brandSub = $(".brand-sub");
  if(brandSub) brandSub.textContent = "Trợ lý AI cá nhân";
}


/* Khởi động toàn diện hệ thống Web */
async function initApp(){
  await ensureValidToken();
  try {
    var meRes = await authedFetch("/api/users/me");
    if(meRes.ok){
      var meData = await meRes.json();
      if(meData && meData.user){
        setRoleView(meData.user.role, meData.user);
      }
    }
  } catch(e){}
  fetchThreads();
  fetchPoolReal();
  fetchUsersReal();
  selectModel("muse", true);
  scanModels();
  renderHistory("");
  renderActivity();
  updateUsage();
  autoGrow();
}

initApp();
})();

