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

var hasMartExtension = false;
var MART_EXTENSION_ID = "mjlkhmjhbhkjfccffeoimoindodmglaj";
function probeMartExtension(){
  requestChromeExtension({ action: "ping" }, 1200).then(function(resp){
    if(resp && resp.ok) hasMartExtension = true;
  }).catch(function(){
    hasMartExtension = false;
  });
}
window.addEventListener("load", probeMartExtension);
setTimeout(probeMartExtension, 1200);
window.addEventListener("message", function(ev){
  if(ev.data && ev.data.__mart_ext_push && (ev.data.action === "MART_EXT_SESSION_READY" || ev.data.event === "MART_EXT_SESSION_READY")){
    hasMartExtension = true;
  }
});

var MASCOT_HTML = '<span class="mascot"><img src="/avatars/mart_mascot.webp" onerror="this.onerror=null;this.src=\'/avatars/mart_cat.png\';" alt="Mart AI" draggable="false"/></span>';
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
  var phases = ["Đang gửi câu hỏi tới máy chủ Mart AI...", "Máy chủ đã kết nối; đang điều phối tới Muse.ai...", "Đang nhận phản hồi từ Muse.ai..."];
  var stepLabels = ["Gửi câu hỏi tới máy chủ Mart AI", "Điều phối câu hỏi tới Muse.ai", "Nhận câu trả lời từ Muse.ai"];
  var stepsHtml = stepLabels.map(function(s, idx){ return '<li data-step="' + idx + '"><span class="ws-ic"></span><span>' + esc(s) + '</span></li>'; }).join("");
  wrap.innerHTML = '<div class="work-stage"><img class="work-avatar" src="/avatars/mart_mascot.webp" onerror="this.onerror=null;this.src=\'/avatars/mart_cat.png\';" alt="Mart AI" width="42" height="42" decoding="async"></div>' +
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
    '<div class="m-av">' + MASCOT_HTML + '</div>' +
    '<div class="m-body">' +
      '<div class="m-name">Mart AI · ' + esc(currentModelShort()) + ' <span>' + nowTime() + '</span></div>' +
      '<div class="bubble"></div>' +
      '<div class="ai-meta"><span class="ai-lbl">Mart AI</span><span class="ai-dot"></span><span class="ai-time">' + nowTime() + '</span></div>' +
    '</div>';
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

function isJwtExpired(token){
  if(!token || typeof token !== "string") return true;
  var parts = token.split(".");
  if(parts.length !== 3) return true;
  try {
    var b64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
    while(b64.length % 4) b64 += "=";
    var jsonStr = decodeURIComponent(escape(atob(b64)));
    var payload = JSON.parse(jsonStr);
    if(!payload.exp) return false;
    var nowSec = Math.floor(Date.now() / 1000);
    return nowSec >= (payload.exp - 30);
  } catch(e){
    return true;
  }
}

function addAiError(msg, retryPrompt){
  var row = createAiMsgContainer();
  var bubble = row.querySelector(".bubble");
  var displayMsg = (msg === "ERR_AUTH_REQUIRED" || msg === "ERR_UNAUTHORIZED" || msg === "ERR_ADMIN_AUTH_REQUIRED")
    ? "Phiên kết nối đã được tự động làm mới. Vui lòng bấm Thử lại để tiếp tục."
    : (msg || "Không thể gửi tin nhắn tới Mart AI");
  var html = '<div class="ai-error-box" style="border:1px solid var(--line-strong);background:var(--surface-2);padding:13px 16px;border-radius:12px;display:flex;flex-direction:column;gap:8px;">' +
    '<div style="display:flex;align-items:center;gap:8px;font-weight:700;color:var(--ink);">' +
      '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" style="color:var(--ink-2);"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>' +
      '<span>Không thể hoàn thành câu trả lời</span>' +
    '</div>' +
    '<div style="font-size:14px;color:var(--ink-2);line-height:1.5;">' + esc(displayMsg) + '</div>';
  if(retryPrompt){
    html += '<div style="margin-top:6px;"><button type="button" class="btn-retry-chat" style="padding:7px 16px;border-radius:8px;border:1px solid var(--brand);background:var(--brand-soft);color:var(--brand-ink);font-weight:700;font-size:13px;cursor:pointer;">Thử lại</button></div>';
  }
  html += '</div>';
  bubble.innerHTML = html;
  if(retryPrompt){
    var btn = bubble.querySelector(".btn-retry-chat");
    if(btn){
      btn.addEventListener("click", function(){
        row.remove();
        localStorage.removeItem("mart_jwt_token");
        ensureValidToken(true).then(function(){
          handleSend(retryPrompt);
        });
      });
    }
  }
  messagesEl.appendChild(row);
  scrollDown();
}

var isSending = false;
var queuedPrompts = [];
var CHAT_REQUEST_TIMEOUT_MS = 200000;

/* Đảm bảo token JWT RS256 luôn hợp lệ trước mọi thao tác */
async function ensureValidToken(forceRefresh, signal){
  var tok = localStorage.getItem("mart_jwt_token") || "";
  if(forceRefresh || !tok || isJwtExpired(tok)){
    try {
      localStorage.removeItem("mart_jwt_token");
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
async function authedFetch(url, opts, isRetry){
  opts = opts || {};
  opts.headers = opts.headers || {};
  var tok = await ensureValidToken();
  if(tok && !opts.headers["Authorization"]) {
    opts.headers["Authorization"] = "Bearer " + tok;
  }
  if(!opts.headers["X-Mart-Client"]){
    opts.headers["X-Mart-Client"] = "web";
  }
  var res = await fetch(url, opts);
  if(res.status === 401 && !isRetry){
    var newTok = await ensureValidToken(true);
    if(newTok){
      opts.headers["Authorization"] = "Bearer " + newTok;
      return authedFetch(url, opts, true);
    }
  }
  return res;
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
    var canRelay = !!hasMartExtension;
    var headers = {
      "Content-Type": "application/json",
      "Accept": "text/event-stream, application/json",
      "X-Mart-Client": "web",
      "X-Mart-Ext-Relay": canRelay ? "1" : "0"
    };
    if(authTok) headers["Authorization"] = "Bearer " + authTok;

    var reqBody = {
      prompt: text,
      thread_id: activeConvId || "",
      model: modelName,
      client_relay_capable: canRelay,
      skip_browser_relay: !canRelay,
      stream: true
    };

    updateTypingProgress(0, "Đang gửi yêu cầu tới Mart AI server; chưa có xác nhận từ server.");
    var resp = await fetch("/api/chat/send", {
      method: "POST",
      headers: headers,
      body: JSON.stringify(reqBody),
      cache: "no-store",
      signal: controller.signal
    });

    // Tự động phục hồi nếu phiên bị hết hạn (HTTP 401 ERR_AUTH_REQUIRED)
    if(resp.status === 401){
      updateTypingProgress(0, "Phiên kết nối đã hết hạn, đang tự động cấp lại phiên mới...");
      var refreshedTok = await ensureValidToken(true, controller.signal);
      if(refreshedTok){
        headers["Authorization"] = "Bearer " + refreshedTok;
        resp = await fetch("/api/chat/send", {
          method: "POST",
          headers: headers,
          body: JSON.stringify(reqBody),
          cache: "no-store",
          signal: controller.signal
        });
      }
    }

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
        // Gia hạn timeout thêm 60 giây mỗi khi nhận dữ liệu từ server (chống timeout giả)
        clearTimeout(requestTimer);
        requestTimer = setTimeout(function(){
          activeStream.timedOut = true;
          controller.abort();
        }, 60000);
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
        var extAvailable = false;
        try {
          var extCheck = await requestChromeExtension({ action: "ping" }, 1500);
          if(extCheck && extCheck.ok && extCheck.supportsChatRelay !== false){
            extAvailable = true;
            hasMartExtension = true;
          }
        } catch(e){
          extAvailable = false;
          hasMartExtension = false;
        }

        if(!extAvailable){
          // Extension khong co san -> Khong quăng loi do! Tu dong fallback ve Server CDP / Failover
          updateTypingProgress(1, "Máy chủ đang tự động điều phối câu trả lời...");
          await authedFetch("/api/chat/relay-complete", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              thread_id: dataJson.thread_id,
              turn_id: dataJson.turn_id,
              node_id: dataJson.node_id,
              model: dataJson.model || modelName,
              error: "client_no_relay_extension"
            })
          }).catch(function(){});

          var fallbackRes = await authedFetch("/api/chat/send", {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "Accept": "application/json",
              "X-Mart-Client": "web",
              "X-Mart-Ext-Relay": "0"
            },
            body: JSON.stringify({
              prompt: text,
              thread_id: dataJson.thread_id || activeConvId || "",
              model: modelName,
              client_relay_capable: false,
              skip_browser_relay: true,
              stream: false
            })
          });
          if(!fallbackRes.ok){
            var fbErr = await fallbackRes.json().catch(function(){ return {}; });
            throw new Error(fbErr.error || "Máy chủ tạm thời bận; vui lòng thử lại sau.");
          }
          dataJson = await fallbackRes.json();
        } else {
          updateTypingProgress(2, "Đang nhận phản hồi từ Muse.ai...");
          var relayOut;
          try {
            relayOut = await requestChromeExtension({
              action: "execute_muse_chat",
              prompt: dataJson.prompt || text,
              cookie: dataJson.relay_cookie || ""
            }, 195000);
            if(!relayOut || !relayOut.ok || !String(relayOut.content || "").trim()){
              throw new Error((relayOut && relayOut.error) || "Không nhận được phản hồi mới từ Muse.ai.");
            }
          } catch(relayErr) {
            var relayError = (relayErr && relayErr.message) || "Không nhận được phản hồi từ Muse.ai.";
            await authedFetch("/api/chat/relay-complete", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ thread_id: dataJson.thread_id, turn_id: dataJson.turn_id, node_id: dataJson.node_id, model: dataJson.model, error: relayError })
            }).catch(function(){});
            throw new Error(relayError);
          }
          updateTypingProgress(2, "Muse.ai đã trả lời; Mart AI đang lưu kết quả.");
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
            throw new Error(completed.error || "Máy chủ chưa lưu được câu trả lời.");
          }
          dataJson = completed;
        }
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
    if(!input.value && lastUserText){
      input.value = lastUserText;
      autoGrow();
    }
    if(activeStream.timedOut){
      addAiError("Thời gian phản hồi từ Muse.ai bị quá hạn. Vui lòng bấm 'Thử lại' bên dưới để hệ thống tự động kết nối lại.", lastUserText);
      toast("Đã dừng yêu cầu do quá thời gian chờ.");
      return;
    }
    if(activeStream.cancelled) return;
    addAiError(err.message || "Không thể hoàn thành câu trả lời", lastUserText);
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
function openModal(){
  modal.classList.add("open");
  if(window.location.pathname !== "/cai-dat"){
    try { history.pushState({modal:"settings"}, "", "/cai-dat"); } catch(e){}
  }
}
function closeModal(){
  modal.classList.remove("open");
  if(window.location.pathname === "/cai-dat"){
    try { history.pushState(null, "", "/"); } catch(e){}
  }
}
$("#footSettings").addEventListener("click", openModal);

window.addEventListener("popstate", function(ev){
  if(window.location.pathname === "/cai-dat"){
    modal.classList.add("open");
  } else {
    modal.classList.remove("open");
  }
  if(window.location.pathname === "/admin"){
    if(typeof openAdmin === "function") openAdmin();
  } else {
    if(typeof closeAdmin === "function") closeAdmin();
  }
});

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
    var resolved = false;
    var timer = setTimeout(function(){
      if(resolved) return;
      resolved = true;
      window.removeEventListener("message", onMessage);
      reject(new Error("Không nhận được phản hồi từ tiện ích Chrome. Hãy cài/bật Mart AI Extension, sau đó tải lại trang."));
    }, timeoutMs || 5000);
    function onMessage(ev){
      if(resolved) return;
      if(ev.source !== window || ev.origin !== window.location.origin || !ev.data || ev.data.__mart_ext_resp !== true || ev.data.reqId !== reqId) return;
      resolved = true;
      clearTimeout(timer);
      window.removeEventListener("message", onMessage);
      if(ev.data.error) reject(new Error(ev.data.error));
      else resolve(ev.data.result || null);
    }
    window.addEventListener("message", onMessage);
    window.postMessage({ __mart_ext_req: true, reqId: reqId, payload: payload }, window.location.origin);

    if(window.chrome && window.chrome.runtime && window.chrome.runtime.sendMessage){
      try {
        window.chrome.runtime.sendMessage(MART_EXTENSION_ID, payload, function(resp){
          if(resolved) return;
          if(!window.chrome.runtime.lastError && resp){
            resolved = true;
            clearTimeout(timer);
            window.removeEventListener("message", onMessage);
            if(resp.error) reject(new Error(resp.error));
            else resolve(resp);
          }
        });
      } catch(e){}
    }
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


/* ============================================================================
   CỬA SỔ ẢO ĐĂNG NHẬP MUSE.AI TRỰC TIẾP TRÊN SERVER (0 CÀI TIỆN ÍCH, 0 F12)
   ============================================================================ */
var remoteLoginSessionId = "";
var remoteLoginPollTimer = null;
var remoteLoginLastSeq = 0;
var remoteVirtualW = 960, remoteVirtualH = 600;
var remoteInputBusy = false;

var serverLoginModal = $("#serverLoginModal");
var btnOpenServerLoginModal = $("#btnOpenServerLoginModal");
var btnCloseServerLogin = $("#btnCloseServerLogin");
var closeServerLoginBg = $("#closeServerLoginBg");
var remoteBrowserLoading = $("#remoteBrowserLoading");
var remoteBrowserImg = $("#remoteBrowserImg");
var remoteLoginStatusText = $("#remoteLoginStatusText");
var remoteBrowserTextInput = $("#remoteBrowserTextInput");
var btnSendRemoteText = $("#btnSendRemoteText");

function closeServerLoginModal(){
  if(serverLoginModal) serverLoginModal.classList.remove("open");
  if(remoteLoginPollTimer){
    clearInterval(remoteLoginPollTimer);
    remoteLoginPollTimer = null;
  }
  if(remoteLoginSessionId){
    authedFetch("/api/accounts/remote-login/cancel", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_id: remoteLoginSessionId })
    }).catch(function(){});
    remoteLoginSessionId = "";
  }
}

if(btnCloseServerLogin) btnCloseServerLogin.addEventListener("click", closeServerLoginModal);
if(closeServerLoginBg) closeServerLoginBg.addEventListener("click", closeServerLoginModal);

async function startServerRemoteLogin(){
  if(!serverLoginModal) return;
  serverLoginModal.classList.add("open");
  if(remoteBrowserLoading) remoteBrowserLoading.style.display = "flex";
  if(remoteBrowserImg) remoteBrowserImg.style.display = "none";
  if(remoteLoginStatusText) remoteLoginStatusText.textContent = "Đang khởi động trình duyệt Server...";

  try {
    var res = await authedFetch("/api/accounts/remote-login/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        label: ($("#accLabel") && $("#accLabel").value.trim()) || "Muse.ai Cloud",
        proxy_url: ($("#accProxy") && $("#accProxy").value.trim()) || ""
      })
    });
    var data = await res.json().catch(function(){ return {}; });
    if(!res.ok || !data.session_id){
      throw new Error(data.error || "Máy chủ chưa sẵn sàng Chromium headless.");
    }

    remoteLoginSessionId = data.session_id;
    if(data.width) remoteVirtualW = data.width;
    if(data.height) remoteVirtualH = data.height;
    remoteLoginLastSeq = 0;
    if(remoteLoginStatusText) remoteLoginStatusText.textContent = "Đã kết nối! Bạn có thể nhấp chuột và gõ phím bên dưới:";

    if(remoteLoginPollTimer) clearInterval(remoteLoginPollTimer);
    remoteLoginPollTimer = setInterval(async function(){
      if(!remoteLoginSessionId) return;
      try {
        var stRes = await authedFetch("/api/accounts/remote-login/state?session_id=" + encodeURIComponent(remoteLoginSessionId) + "&after_seq=" + remoteLoginLastSeq);
        if(!stRes.ok) return;
        var snap = await stRes.json().catch(function(){ return {}; });
        if(!snap) return;

        if(snap.width) remoteVirtualW = snap.width;
        if(snap.height) remoteVirtualH = snap.height;
        if(snap.frame_seq) remoteLoginLastSeq = snap.frame_seq;

        if(!snap.unchanged && snap.frame && remoteBrowserImg){
          remoteBrowserImg.src = snap.frame;
          remoteBrowserImg.style.display = "block";
          if(remoteBrowserLoading) remoteBrowserLoading.style.display = "none";
        }

        if(snap.status === "completed"){
          closeServerLoginModal();
          toast("✅ Đã bắt phiên Muse.ai thành công và lưu vào Pool!");
          await fetchPoolReal();
          return;
        }

        if(snap.status === "error" || snap.status === "cancelled"){
          var errTxt = snap.error || "Phiên đăng nhập đã kết thúc.";
          if(remoteLoginStatusText) remoteLoginStatusText.textContent = "Lỗi: " + errTxt;
        }
      } catch(e){}
    }, 700);

  } catch(err){
    if(remoteLoginStatusText) remoteLoginStatusText.textContent = "Lỗi: " + (err.message || "Không thể mở trình duyệt Server");
    toast(err.message || "Không khởi động được phiên đăng nhập trên Server.");
  }
}

if(btnOpenServerLoginModal){
  btnOpenServerLoginModal.addEventListener("click", startServerRemoteLogin);
}

if(remoteBrowserImg){
  remoteBrowserImg.addEventListener("click", async function(e){
    if(!remoteLoginSessionId || remoteInputBusy) return;
    var rect = remoteBrowserImg.getBoundingClientRect();
    if(!rect.width || !rect.height) return;
    var relX = Math.max(0, Math.min(rect.width, e.clientX - rect.left));
    var relY = Math.max(0, Math.min(rect.height, e.clientY - rect.top));
    var virtX = Math.round((relX / rect.width) * remoteVirtualW);
    var virtY = Math.round((relY / rect.height) * remoteVirtualH);

    remoteInputBusy = true;
    try {
      await authedFetch("/api/accounts/remote-login/input", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          session_id: remoteLoginSessionId,
          type: "click",
          x: virtX,
          y: virtY
        })
      });
    } catch(err){}
    finally { remoteInputBusy = false; }
  });
}

function sendRemoteTextFromInput(){
  if(!remoteLoginSessionId || !remoteBrowserTextInput) return;
  var txt = remoteBrowserTextInput.value;
  if(!txt) return;
  remoteBrowserTextInput.value = "";
  authedFetch("/api/accounts/remote-login/input", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      session_id: remoteLoginSessionId,
      type: "text",
      text: txt
    })
  }).catch(function(){});
}

if(btnSendRemoteText) btnSendRemoteText.addEventListener("click", sendRemoteTextFromInput);
if(remoteBrowserTextInput){
  remoteBrowserTextInput.addEventListener("keydown", function(e){
    if(e.key === "Enter"){
      e.preventDefault();
      sendRemoteTextFromInput();
    }
  });
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
  if(window.location.pathname === "/cai-dat"){
    setTimeout(function(){ openModal(); }, 100);
  } else if(window.location.pathname === "/admin"){
    setTimeout(function(){ openAdmin(); closeDrawers(); }, 100);
  }
}

initApp();
})();

