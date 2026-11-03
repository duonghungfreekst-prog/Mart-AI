/* ============================================================================
   Chat — Muse Frontend Application (app.js)
   Giao diện chuẩn xác 100% theo https://muse.ai — Kết nối đa tài khoản thật
   ============================================================================ */

(function () {
  "use strict";

  // Xóa / khóa hoàn toàn chế độ Zoom (phóng to & thu nhỏ trang) trên toàn bộ trang web:
  // 1) Chặn giữ Ctrl/Meta + lăn chuột hoặc chụm/tách 2 ngón trên trackpad
  window.addEventListener(
    "wheel",
    function (ev) {
      if (ev.ctrlKey || ev.metaKey) {
        ev.preventDefault();
      }
    },
    { passive: false }
  );

  // 2) Chặn phím tắt phóng to / thu nhỏ trang (Ctrl/Cmd + '+', '-', '=', '_', '0')
  window.addEventListener(
    "keydown",
    function (ev) {
      if (ev.ctrlKey || ev.metaKey) {
        var k = ev.key;
        var c = ev.code;
        if (
          k === "+" ||
          k === "-" ||
          k === "=" ||
          k === "_" ||
          k === "0" ||
          c === "Equal" ||
          c === "Minus" ||
          c === "Digit0" ||
          c === "NumpadAdd" ||
          c === "NumpadSubtract" ||
          c === "Numpad0"
        ) {
          ev.preventDefault();
        }
      }
    },
    { capture: true }
  );

  // 3) Chặn cử chỉ pinch-to-zoom và double-tap zoom trên màn hình cảm ứng / trackpad
  ["gesturestart", "gesturechange", "gestureend"].forEach(function (evtName) {
    window.addEventListener(
      evtName,
      function (ev) {
        ev.preventDefault();
      },
      { passive: false }
    );
  });

  window.addEventListener(
    "touchmove",
    function (ev) {
      if (ev.touches && ev.touches.length > 1) {
        ev.preventDefault();
      }
    },
    { passive: false }
  );

  const state = {
    viewMode: "chat",
    summary: {
      total_nodes: 0,
      running_nodes: 0,
      ready_nodes: 0,
      unavailable_nodes: 0,
      active_requests: 0,
      queued_requests: 0,
      total_tokens_remaining: 0,
      formatted_tokens: "0",
      routing_strategy: "least_active",
      nodes: [],
    },
    threads: [],
    activeThreadId: "",
    pendingAttachments: [],
    jwtToken: "",
    lastOAuthSessionId: "",
    lastOAuthLoginUrl: "",
    lastNodesSignature: "",
    bgActive: false,
    pendingChatCount: 0,
  };

  function showToast(message) {
    const region = document.getElementById("toastRegion");
    if (!region) return;
    const text = String(message || "");
    const existingCards = Array.from(region.querySelectorAll(".toast-card"));
    for (let i = 0; i < existingCards.length; i++) {
      if (existingCards[i].textContent === text) {
        return;
      }
    }
    while (region.children.length >= 2) {
      region.removeChild(region.firstElementChild);
    }
    const card = document.createElement("div");
    card.className = "toast-card";
    card.textContent = text;
    region.appendChild(card);
    setTimeout(function () {
      if (card.parentNode) {
        card.parentNode.removeChild(card);
      }
    }, 3800);
  }

  function escapeHtml(raw) {
    return String(raw || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function formatInlineMarkdown(line) {
    let out = escapeHtml(line);
    out = out.replace(
      /`([^`]+)`/g,
      '<code style="background:rgba(0,0,0,0.06);padding:1px 5px;border-radius:4px;font-family:var(--font-mono);font-size:0.86em;">$1</code>'
    );
    out = out.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    out = out.replace(/\*([^*\n]+)\*/g, "<em>$1</em>");
    return out;
  }

  /**
   * Chuyển đổi văn bản / Markdown phản hồi từ Muse.ai sang cấu trúc HTML chuẩn
   * (<p>, <strong>, <ul><li> cấp 1 • và <ul><ul><li> cấp 2 ◦ giống hệt https://muse.ai)
   */
  function formatMarkdownNative(rawText) {
    const text = String(rawText || "").replace(/\r\n/g, "\n");
    if (!text.trim()) return "";

    const blocks = text.split(/(```[\s\S]*?```)/g);
    const htmlParts = [];

    blocks.forEach(function (block) {
      if (block.startsWith("```") && block.endsWith("```")) {
        const firstNewline = block.indexOf("\n");
        const codeContent =
          firstNewline >= 0 ? block.slice(firstNewline + 1, -3) : block.slice(3, -3);
        htmlParts.push(
          '<pre style="background:#1e293b;color:#f8fafc;padding:12px;border-radius:10px;overflow-x:auto;margin:8px 0;font-family:var(--font-mono);font-size:12.5px;"><code>' +
            escapeHtml(codeContent.trim()) +
            "</code></pre>"
        );
        return;
      }

      const lines = block.split("\n");
      let inTopList = false;
      let inSubList = false;
      let paraBuffer = [];

      function flushPara() {
        if (paraBuffer.length > 0) {
          htmlParts.push("<p>" + paraBuffer.join("<br>") + "</p>");
          paraBuffer = [];
        }
      }

      function closeLists() {
        if (inSubList) {
          htmlParts.push("</ul></li>");
          inSubList = false;
        }
        if (inTopList) {
          htmlParts.push("</ul>");
          inTopList = false;
        }
      }

      lines.forEach(function (line) {
        const subMatch = line.match(/^(?:\s{2,}|\t+)(?:[-*•◦]|\d+\.)\s+(.*)$/);
        const topMatch = !subMatch && line.match(/^\s?(?:[-*•]|\d+\.)\s+(.*)$/);

        if (subMatch) {
          flushPara();
          if (!inTopList) {
            htmlParts.push("<ul><li>");
            inTopList = true;
          }
          if (!inSubList) {
            if (htmlParts.length > 0 && htmlParts[htmlParts.length - 1].endsWith("</li>")) {
              htmlParts[htmlParts.length - 1] = htmlParts[htmlParts.length - 1].slice(0, -5);
            }
            htmlParts.push("<ul>");
            inSubList = true;
          }
          htmlParts.push("<li>" + formatInlineMarkdown(subMatch[1]) + "</li>");
        } else if (topMatch) {
          flushPara();
          if (inSubList) {
            htmlParts.push("</ul></li>");
            inSubList = false;
          }
          if (!inTopList) {
            htmlParts.push("<ul>");
            inTopList = true;
          }
          htmlParts.push("<li>" + formatInlineMarkdown(topMatch[1]) + "</li>");
        } else if (!line.trim()) {
          closeLists();
          flushPara();
        } else {
          closeLists();
          paraBuffer.push(formatInlineMarkdown(line));
        }
      });

      closeLists();
      flushPara();
    });

    return htmlParts.join("");
  }

  async function apiRequest(path, options) {
    const opts = options || {};
    const headers = Object.assign(
      {
        "Content-Type": "application/json",
      },
      opts.headers || {}
    );
    if (state.jwtToken) {
      headers["Authorization"] = "Bearer " + state.jwtToken;
    }
    const resp = await fetch(path, {
      method: opts.method || "GET",
      headers: headers,
      body: opts.body ? JSON.stringify(opts.body) : undefined,
      cache: "no-store",
    });
    const data = await resp.json().catch(function () {
      return {};
    });
    if (!resp.ok) {
      throw new Error(data.error || "ERR_HTTP_" + resp.status);
    }
    return data;
  }

  // ============================================================================
  // 0.5. CHUẨN LIBSQL WEB CLIENT TRỰC TIẾP (HRANA V2 HTTP PIPELINE /v2/pipeline)
  // ============================================================================

  const libsqlClient = {
    pipelineEndpoint: "/v2/pipeline",

    encodeArg: function (val) {
      if (val === null || val === undefined) {
        return { type: "null" };
      }
      if (typeof val === "number") {
        if (Number.isInteger(val)) {
          return { type: "integer", value: String(val) };
        }
        return { type: "float", value: val };
      }
      return { type: "text", value: String(val) };
    },

    execute: async function (sql, args) {
      const startMs = performance.now();
      const encodedArgs = (args || []).map(libsqlClient.encodeArg);
      const payload = {
        requests: [
          {
            type: "execute",
            stmt: {
              sql: sql,
              args: encodedArgs,
              want_rows: true,
            },
          },
          {
            type: "close",
          },
        ],
      };

      const resp = await apiRequest(libsqlClient.pipelineEndpoint, {
        method: "POST",
        body: payload,
      });

      const first = (resp.results || [])[0];
      if (!first) {
        throw new Error("Phản hồi Hrana Pipeline rỗng");
      }
      if (first.type === "error") {
        throw new Error((first.error && first.error.message) || "Lỗi thực thi SQL trên libSQL");
      }

      const result = (first.response && first.response.result) || {};
      const cols = (result.cols || []).map(function (c) {
        return c.name;
      });
      const rows = (result.rows || []).map(function (r) {
        return r.map(function (cell) {
          if (!cell || cell.type === "null") return null;
          return cell.value;
        });
      });

      return {
        columns: cols,
        rows: rows,
        rowsAffected: result.affected_row_count || 0,
        lastInsertRowid: result.last_insert_rowid || "0",
        elapsedMs: Math.round(performance.now() - startMs),
      };
    },
  };

  window.MuseLibSQLClient = libsqlClient;

  async function refreshLibSQLStatus() {
    try {
      const st = await apiRequest("/api/libsql/status");
      const sideBadge = document.getElementById("sideLibsqlBadge");
      if (sideBadge) {
        const tblSummary = (st.tables || [])
          .map(function (t) {
            return t.table_name + ": " + t.row_count;
          })
          .join(" • ");
        sideBadge.textContent =
          (st.engine_version || "libSQL") +
          " (" +
          (st.journal_mode || "WAL") +
          ") — " +
          (st.database_url || "") +
          (tblSummary ? " [" + tblSummary + "]" : "");
      }

      const urlInput = document.getElementById("libsqlUrlInput");
      if (urlInput && !urlInput.value && st.database_url) {
        urlInput.value = st.database_url;
      }

      const subtext = document.getElementById("libsqlStatusSubtext");
      if (subtext && st.connected) {
        const tblCounts = (st.tables || [])
          .map(function (t) {
            return t.table_name + " (" + t.row_count + ")";
          })
          .join(" · ");
        subtext.innerHTML =
          "Đang kết nối trực tiếp: <code>" +
          escapeHtml(st.database_url || "") +
          "</code> • Chế độ: <strong>" +
          escapeHtml(st.journal_mode || "WAL") +
          "</strong> • Engine: <strong>" +
          escapeHtml(st.engine_version || "libSQL") +
          "</strong> • Các bảng: " +
          escapeHtml(tblCounts);
      }
    } catch (err) {
      console.warn("LibSQL status check warning:", err);
    }
  }

  async function executeAndRenderLibSQLQuery(sqlText) {
    const headEl = document.getElementById("libsqlResultHead");
    const bodyEl = document.getElementById("libsqlResultBody");
    const metaEl = document.getElementById("libsqlQueryMeta");
    if (!headEl || !bodyEl) return;

    try {
      const res = await libsqlClient.execute(sqlText, []);
      if (metaEl) {
        metaEl.textContent =
          "✅ Giao thức chuẩn libSQL Hrana v2 (/v2/pipeline) • " +
          res.rows.length +
          " dòng trả về • " +
          res.rowsAffected +
          " dòng tác động • " +
          res.elapsedMs +
          "ms";
      }

      if (res.columns.length === 0) {
        headEl.innerHTML = "";
        bodyEl.innerHTML =
          '<tr><td style="padding:12px;color:#16a34a;">Thực thi câu lệnh thành công (' +
          res.rowsAffected +
          " bản ghi được cập nhật).</td></tr>";
        await refreshLibSQLStatus();
        return;
      }

      headEl.innerHTML =
        "<tr>" +
        res.columns
          .map(function (c) {
            return "<th>" + escapeHtml(c) + "</th>";
          })
          .join("") +
        "</tr>";

      if (res.rows.length === 0) {
        bodyEl.innerHTML =
          '<tr><td colspan="' +
          res.columns.length +
          '" style="text-align:center;color:#737373;padding:16px;">Bảng hiện chưa có bản ghi nào.</td></tr>';
        return;
      }

      bodyEl.innerHTML = res.rows
        .map(function (row) {
          return (
            "<tr>" +
            row
              .map(function (cell) {
                return (
                  "<td style='font-family:var(--font-mono);font-size:12px;'>" +
                  escapeHtml(cell === null ? "NULL" : String(cell)) +
                  "</td>"
                );
              })
              .join("") +
            "</tr>"
          );
        })
        .join("");
    } catch (err) {
      if (metaEl) {
        metaEl.textContent = "❌ Lỗi truy vấn libSQL: " + err.message;
      }
      showToast("Lỗi thực thi libSQL: " + err.message);
    }
  }

  // ============================================================================
  // 1. ĐỒNG BỘ TRẠNG THÁI TÀI KHOẢN TỪ GO ENGINE
  // ============================================================================

  async function refreshRouterState(forceRender) {
    const offlineBanner = document.getElementById("offlineBanner");
    try {
      const data = await apiRequest("/api/router/summary");
      if (offlineBanner) offlineBanner.classList.add("hidden-el");
      state.summary = data;

      const total = data.total_nodes || 0;
      const railNodeCount = document.getElementById("railNodeCount");
      if (railNodeCount) {
        railNodeCount.textContent = String(total);
        railNodeCount.classList.toggle("hidden-el", total === 0);
      }

      const sideNodeCount = document.getElementById("sideNodeCount");
      if (sideNodeCount) {
        sideNodeCount.textContent = String(total);
      }

      const sig = JSON.stringify(data.nodes || []);
      if (forceRender || sig !== state.lastNodesSignature) {
        state.lastNodesSignature = sig;
        populateNodePinSelector();
        renderSideNodesList();
        renderAccountsTable();
      }
      updateMuseSidebarStatus();
    } catch (err) {
      if (offlineBanner) offlineBanner.classList.remove("hidden-el");
    }
  }

  function populateNodePinSelector() {
    const syncBtn = document.getElementById("btnSyncAccountsPool");
    if (!syncBtn) return;
    const total = state.summary.total_nodes || 0;
    const ready = (state.summary.ready_nodes || 0) + (state.summary.running_nodes || 0);
    if (total === 0) {
      syncBtn.textContent = "🔄 Đồng bộ tài khoản";
    } else {
      syncBtn.textContent = "🔄 Đồng bộ tài khoản (" + ready + "/" + total + " gộp Token)";
    }
  }

  function updateMuseSidebarStatus() {
    if (state.isGenerating) return;
    const dot = document.getElementById("museConnDot");
    const actionIcon = document.getElementById("museActionIcon");
    const txt = document.getElementById("museConnText");
    if (!dot || !txt) return;

    if (actionIcon) actionIcon.classList.add("hidden-el");
    dot.classList.remove("hidden-el");

    const total = state.summary.total_nodes || 0;
    const activeReady = (state.summary.ready_nodes || 0) + (state.summary.running_nodes || 0);

    if (total === 0) {
      dot.className = "muse-status-badge-icon disconnected";
      txt.textContent = "Chưa kết nối";
      return;
    }

    if (activeReady > 0) {
      dot.className = "muse-status-badge-icon connected";
      txt.textContent = "Đã kết nối";
    } else {
      dot.className = "muse-status-badge-icon disconnected";
      txt.textContent = "Chưa kết nối";
    }
  }

  function inferMuseActivityBadge(textOrStatus, isDirectDomStatus) {
    const raw = String(textOrStatus || "").trim();
    const lower = raw.toLowerCase();

    if (isDirectDomStatus && raw.length > 0 && raw.length <= 42) {
      return { icon: "🐱", label: raw };
    }

    if (/ảnh|hình|vẽ|minh họa|logo|banner|photo|image|poster|avatar/i.test(lower)) {
      return { icon: "🐱", label: "Đang lấy hình ảnh vào xe" };
    }
    if (/code|lập trình|web|html|css|js|python|sql|app|trang|tính năng|script/i.test(lower)) {
      return { icon: "🐱", label: "Đang chất mã nguồn lên xe" };
    }
    return { icon: "🐱", label: "Đang đẩy xe lấy hàng" };
  }

  function updateMartCartProgress(pct, actionLabel) {
    const clamped = Math.max(0, Math.min(100, Number(pct || 0)));
    const fillEl = document.getElementById("martCartProgressFill");
    if (fillEl) {
      const w = Math.round((clamped / 100) * 64);
      fillEl.setAttribute("width", String(w));
    }

    const thresholds = [12, 28, 45, 62, 78, 92];
    for (let i = 0; i < thresholds.length; i++) {
      const itemEl = document.getElementById("martItem" + (i + 1));
      if (itemEl) {
        if (clamped >= thresholds[i]) {
          itemEl.classList.add("visible");
          itemEl.style.opacity = "1";
        } else {
          itemEl.classList.remove("visible");
          itemEl.style.opacity = "0";
        }
      }
    }

    if (clamped > 0) {
      const dot = document.getElementById("museConnDot");
      const actionIcon = document.getElementById("museActionIcon");
      const txt = document.getElementById("museConnText");
      if (dot) dot.classList.add("hidden-el");
      if (actionIcon) {
        actionIcon.textContent = clamped >= 95 ? "🛒" : "🐱";
        actionIcon.classList.remove("hidden-el");
      }
      if (txt) {
        const baseLbl = actionLabel || "Đang đẩy xe lấy hàng";
        txt.textContent = baseLbl + " (" + Math.round(clamped) + "%)";
      }
    }
  }

  function setMuseCharacterActivity(isActive, promptOrStatus, avatarFrame, isDirectDomStatus, progressPct) {
    const svgIdle = document.getElementById("museSvgIdle");
    const svgActive = document.getElementById("museSvgActive");
    const liveImg = document.getElementById("museLiveAvatarImg");
    const stopBtn = document.getElementById("btnStopGenerating");

    if (isActive) {
      state.isGenerating = true;
      if (stopBtn) stopBtn.classList.remove("hidden-el");

      const badge = inferMuseActivityBadge(promptOrStatus, Boolean(isDirectDomStatus));

      if (liveImg) liveImg.classList.add("hidden-el");
      if (svgIdle) svgIdle.classList.add("hidden-el");
      if (svgActive) svgActive.classList.remove("hidden-el");

      const pctToUse = typeof progressPct === "number" ? progressPct : 14;
      updateMartCartProgress(pctToUse, badge.label);
    } else {
      state.isGenerating = false;
      if (stopBtn) stopBtn.classList.add("hidden-el");
      if (liveImg) liveImg.classList.add("hidden-el");
      updateMartCartProgress(0, "");
      if (svgActive) {
        svgActive.classList.add("hidden-el");
      }
      if (svgIdle) {
        svgIdle.classList.remove("hidden-el");
      }
      updateMuseSidebarStatus();
    }
  }

  function renderSideNodesList() {
    const container = document.getElementById("sideNodesList");
    if (!container) return;

    const nodes = state.summary.nodes || [];
    if (nodes.length === 0) {
      container.innerHTML =
        '<div style="font-size:12.5px;color:#737373;padding:6px 2px;">' +
        "Chưa có tài khoản nào. Bấm sang tab cuối bên phải hoặc biểu tượng lưới ở thanh bên trái để cắm tài khoản gộp Token." +
        "</div>";
      return;
    }

    container.innerHTML = "";
    nodes.forEach(function (n) {
      const item = document.createElement("div");
      item.className = "today-task-item";
      const isOk = n.status === "ready" || n.status === "running";
      const shieldInfo = n.proxy_url
        ? "🌐 Proxy riêng · " + (n.device_profile || "Cô lập vân tay")
        : "🛡️ Trùng IP An Toàn · " + (n.device_profile || "Cô lập datr/Canvas/WebGL");

      item.innerHTML =
        '<div class="today-task-icon" style="color:' +
        (isOk ? "#16a34a" : "#737373") +
        ';">' +
        '  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '    <rect x="3" y="3" width="18" height="18" rx="4"/>' +
        '    <polyline points="9 12 11 14 15 10"/>' +
        "  </svg>" +
        "</div>" +
        '<div class="today-task-body">' +
        '  <div class="today-task-title">' +
        escapeHtml(n.label) +
        " · <span style='font-weight:500;color:#16a34a;'>Gộp Token</span></div>" +
        '  <div class="today-task-sub">' +
        escapeHtml(shieldInfo) +
        "</div>" +
        '  <div class="today-task-time">' +
        escapeHtml(n.status_detail || (isOk ? "Đang đồng bộ gộp Token" : "Chưa đồng bộ")) +
        "</div>" +
        "</div>";

      item.addEventListener("click", async function () {
        showToast("🔄 Đang đồng bộ lại Token của " + n.label + "...");
        try {
          const res = await apiRequest("/api/accounts/wake", {
            method: "POST",
            body: { node_id: n.id },
          });
          showToast("✅ Đã đồng bộ " + n.label + " vào Pool Token chung (" + (res.detail || "Sẵn sàng") + ")");
          await refreshRouterState(true);
        } catch (err) {
          showToast("Lỗi đồng bộ " + n.label + ": " + err.message);
        }
      });

      container.appendChild(item);
    });
  }

  // ============================================================================
  // 2. DANH SÁCH HÔM NAY & LUỒNG TIN NHẮN (GIỐNG 100% HTTPS://MUSE.AI)
  // ============================================================================

  async function loadThreads() {
    try {
      const data = await apiRequest("/api/threads");
      state.threads = data.threads || [];
      if (state.threads.length > 0 && !state.activeThreadId) {
        state.activeThreadId = state.threads[0].id;
      } else if (state.threads.length === 0) {
        state.activeThreadId = "";
      }
      renderTodayActivityLists();
      renderActiveThreadMessages();
    } catch (err) {
      console.error("Failed to load threads:", err);
    }
  }

  function buildTodayTaskCard(t, onAfterSelect) {
    const item = document.createElement("div");
    item.className = "today-task-item" + (t.id === state.activeThreadId ? " active" : "");
    item.setAttribute("role", "listitem");

    const msgs = t.messages || [];
    const lastMsg = msgs.length > 0 ? msgs[msgs.length - 1] : null;
    const subText = lastMsg
      ? String(lastMsg.content || "").replace(/\s+/g, " ").slice(0, 52)
      : "Đoạn chat mới";
    const timeText = lastMsg && lastMsg.timestamp ? lastMsg.timestamp : "";

    item.innerHTML =
      '<div class="today-task-icon">' +
      '  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round">' +
      '    <rect x="4" y="4" width="16" height="16" rx="4"/>' +
      '    <polyline points="9 12 11 14 15 10"/>' +
      "  </svg>" +
      "</div>" +
      '<div class="today-task-body">' +
      '  <div class="today-task-title">' +
      escapeHtml(t.title || "Đoạn chat") +
      "</div>" +
      '  <div class="today-task-sub">' +
      escapeHtml(subText) +
      "</div>" +
      (timeText ? '  <div class="today-task-time">' + escapeHtml(timeText) + "</div>" : "") +
      "</div>";

    item.addEventListener("click", function () {
      state.activeThreadId = t.id;
      renderTodayActivityLists();
      renderActiveThreadMessages();
      if (typeof onAfterSelect === "function") {
        onAfterSelect();
      }
    });

    return item;
  }

  function renderTodayActivityLists() {
    const rightList = document.getElementById("threadsList");
    const popList = document.getElementById("popoverThreadsList");

    if (rightList) {
      rightList.innerHTML = "";
      if (state.threads.length === 0) {
        rightList.innerHTML =
          '<div style="font-size:12.5px;color:#8c8c8c;padding:4px 2px;">Chưa có hoạt động nào hôm nay.</div>';
      } else {
        state.threads.forEach(function (t) {
          rightList.appendChild(buildTodayTaskCard(t));
        });
      }
    }

    if (popList) {
      popList.innerHTML = "";
      if (state.threads.length === 0) {
        popList.innerHTML =
          '<div style="font-size:12.5px;color:#8c8c8c;padding:6px 4px;">Chưa có đoạn chat nào.</div>';
      } else {
        state.threads.forEach(function (t) {
          const card = buildTodayTaskCard(t, function () {
            const pop = document.getElementById("threadsPopover");
            if (pop) pop.classList.add("hidden-el");
          });
          popList.appendChild(card);
        });
      }
    }
  }

  function fillAiBubbleRow(row, content, nodeLabel, prevUserPrompt) {
    row.className = "ai-bubble-row";
    row.innerHTML = "";

    const card = document.createElement("div");
    card.className = "ai-bubble-card";
    card.innerHTML = formatMarkdownNative(content || "");
    row.appendChild(card);

    // 3 icon nhỏ nằm ngang bên phải đáy bong bóng xám (😊 ↻ •••) giống hệt ảnh chụp https://muse.ai
    const actions = document.createElement("div");
    actions.className = "ai-side-actions";

    // 1. Icon mặt cười 😊
    const reactBtn = document.createElement("button");
    reactBtn.type = "button";
    reactBtn.className = "ai-icon-btn";
    reactBtn.title = "Bày tỏ cảm xúc";
    reactBtn.innerHTML =
      '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
      '<circle cx="12" cy="12" r="10"/>' +
      '<path d="M8 14s1.5 2 4 2 4-2 4-2"/>' +
      '<line x1="9" y1="9" x2="9.01" y2="9"/>' +
      '<line x1="15" y1="9" x2="15.01" y2="9"/>' +
      "</svg>";
    reactBtn.addEventListener("click", function () {
      showToast("Đã ghi nhận phản hồi cho câu trả lời.");
    });

    // 2. Icon thử lại ↻
    const retryBtn = document.createElement("button");
    retryBtn.type = "button";
    retryBtn.className = "ai-icon-btn";
    retryBtn.title = "Tạo lại phản hồi";
    retryBtn.innerHTML =
      '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
      '<polyline points="23 4 23 10 17 10"/>' +
      '<path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>' +
      "</svg>";
    retryBtn.addEventListener("click", function () {
      if (prevUserPrompt) {
        const promptEl = document.getElementById("chatPromptInput");
        if (promptEl) {
          promptEl.value = prevUserPrompt;
          promptEl.dispatchEvent(new Event("input"));
          promptEl.focus();
        }
      }
    });

    // 3. Icon ba chấm ••• (Sao chép nội dung)
    const moreBtn = document.createElement("button");
    moreBtn.type = "button";
    moreBtn.className = "ai-icon-btn";
    moreBtn.title = "Sao chép nội dung" + (nodeLabel ? " (" + nodeLabel + ")" : "");
    moreBtn.innerHTML =
      '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
      '<circle cx="12" cy="12" r="1"/>' +
      '<circle cx="19" cy="12" r="1"/>' +
      '<circle cx="5" cy="12" r="1"/>' +
      "</svg>";
    moreBtn.addEventListener("click", function () {
      navigator.clipboard.writeText(content || "").then(function () {
        showToast("Đã sao chép nội dung phản hồi.");
      });
    });

    actions.appendChild(reactBtn);
    actions.appendChild(retryBtn);
    actions.appendChild(moreBtn);
    row.appendChild(actions);
  }

  function mergeThreadPreservingCompletedTurns(oldThread, newThread) {
    if (!oldThread || !oldThread.messages || !newThread || !newThread.messages) {
      return newThread;
    }
    const completedByTurn = {};
    oldThread.messages.forEach(function (m) {
      if (m.role !== "user" && m.turn_id && !m.pending && String(m.content || "").trim()) {
        completedByTurn[m.turn_id] = m;
      }
    });
    newThread.messages = newThread.messages.map(function (m) {
      if (m.role !== "user" && m.turn_id && (m.pending || !String(m.content || "").trim())) {
        if (completedByTurn[m.turn_id]) {
          return completedByTurn[m.turn_id];
        }
      }
      return m;
    });
    return newThread;
  }

  function renderActiveThreadMessages() {
    const viewport = document.getElementById("chatMessagesViewport");
    if (!viewport) return;

    // welcome screen nằm NGOÀI viewport (sibling) → không bị xóa bởi viewport.innerHTML
    const welcome = document.getElementById("chatWelcomeScreen");
    const current = state.threads.find(function (t) {
      return t.id === state.activeThreadId;
    });
    const hasThread = !!current;

    if (welcome) {
      // Welcome screen: flex khi không có thread, none khi có thread
      welcome.style.display = hasThread ? "none" : "flex";
    }
    // Viewport: ẩn khi không có thread, hiện khi có thread
    if (!hasThread) {
      viewport.classList.add("hidden-el");
      return;
    }
    viewport.classList.remove("hidden-el");

    if (!current || !current.messages || current.messages.length === 0) {
      viewport.innerHTML = "";
      return;
    }

    viewport.innerHTML = "";
    const streamInner = document.createElement("div");
    streamInner.className = "chat-stream-inner";

    current.messages.forEach(function (m, msgIdx) {
      if (m.role === "user") {
        const row = document.createElement("div");
        row.className = "user-bubble-row";
        if (m.turn_id) row.setAttribute("data-turn-id", m.turn_id);

        const card = document.createElement("div");
        card.className = "user-bubble-card";

        if (m.attachments && m.attachments.length > 0) {
          const attGrid = document.createElement("div");
          attGrid.className = "msg-attachments-grid";
          m.attachments.forEach(function (att) {
            if (att.mime_type && att.mime_type.indexOf("image/") === 0 && att.data_url) {
              const img = document.createElement("img");
              img.src = att.data_url;
              img.alt = att.name || "Ảnh đính kèm";
              attGrid.appendChild(img);
            } else {
              const pill = document.createElement("span");
              pill.className = "file-tag-pill";
              pill.textContent = "📎 " + (att.name || "Tệp đính kèm");
              attGrid.appendChild(pill);
            }
          });
          card.appendChild(attGrid);
        }

        if (m.content) {
          const textDiv = document.createElement("div");
          textDiv.innerHTML = escapeHtml(m.content).replace(/\n/g, "<br>");
          card.appendChild(textDiv);
        }

        const wrap = document.createElement("div");
        wrap.className = "user-bubble-wrap";
        wrap.appendChild(card);

        // Huy hiệu 👍 chính gốc từ https://muse.ai/images/reactions/facebook_thumbs_up_v1.png
        const thumbBadge = document.createElement("span");
        thumbBadge.className = "user-bubble-reaction";
        thumbBadge.innerHTML =
          '<img src="/avatars/facebook_thumbs_up_v1.png" width="13" height="13" style="width:13px;height:13px;object-fit:contain;display:block;" alt="👍">';
        wrap.appendChild(thumbBadge);

        row.appendChild(wrap);
        streamInner.appendChild(row);
      } else {
        const row = document.createElement("div");
        if (m.turn_id) row.setAttribute("data-turn-id", m.turn_id);

        if (m.pending || !String(m.content || "").trim()) {
          // Dòng trả lời để trống ngay bên dưới câu hỏi đang chờ phản hồi
          row.className = "ai-bubble-row pending-answer-row";
          const aiCard = document.createElement("div");
          aiCard.className = "ai-bubble-card typing-dots-bubble";
          aiCard.innerHTML =
            '<span class="typing-dot"></span>' +
            '<span class="typing-dot"></span>' +
            '<span class="typing-dot"></span>';
          row.appendChild(aiCard);
          streamInner.appendChild(row);
          return;
        }

        let prevUserPrompt = "";
        for (let i = msgIdx - 1; i >= 0; i--) {
          if (current.messages[i] && current.messages[i].role === "user") {
            prevUserPrompt = current.messages[i].content || "";
            break;
          }
        }
        fillAiBubbleRow(row, m.content || "", m.node_label || "", prevUserPrompt);
        streamInner.appendChild(row);
      }
    });

    viewport.appendChild(streamInner);
    viewport.scrollTop = viewport.scrollHeight;
  }

  function renderAttachmentPreviews() {
    const bar = document.getElementById("attachmentPreviewBar");
    if (!bar) return;

    if (state.pendingAttachments.length === 0) {
      bar.classList.add("hidden-el");
      bar.innerHTML = "";
      return;
    }

    bar.classList.remove("hidden-el");
    bar.innerHTML = "";

    state.pendingAttachments.forEach(function (att, idx) {
      const box = document.createElement("div");
      box.className = "preview-thumb-box";

      if (att.mime_type && att.mime_type.indexOf("image/") === 0) {
        const img = document.createElement("img");
        img.src = att.data_url;
        img.alt = att.name;
        box.appendChild(img);
      }

      const nameSpan = document.createElement("span");
      nameSpan.textContent = att.name;
      box.appendChild(nameSpan);

      const rmBtn = document.createElement("button");
      rmBtn.type = "button";
      rmBtn.className = "preview-del-btn";
      rmBtn.textContent = "✕";
      rmBtn.addEventListener("click", function () {
        state.pendingAttachments.splice(idx, 1);
        renderAttachmentPreviews();
      });
      box.appendChild(rmBtn);

      bar.appendChild(box);
    });
  }

  function addFilesToAttachments(fileList) {
    Array.from(fileList || []).forEach(function (file) {
      if (file.size > 10 * 1024 * 1024) {
        showToast("Tệp " + file.name + " vượt quá giới hạn 10MB.");
        return;
      }
      const reader = new FileReader();
      reader.onload = function (ev) {
        state.pendingAttachments.push({
          name: file.name || "image.png",
          mime_type: file.type || "application/octet-stream",
          data_url: String(ev.target.result || ""),
        });
        renderAttachmentPreviews();
      };
      reader.readAsDataURL(file);
    });
  }

  // ============================================================================
  // 3. BẢNG QUẢN LÝ ĐA TÀI KHOẢN MUSE.AI
  // ============================================================================

  function formatNumWithCommas(val) {
    const n = Number(val || 0);
    if (!isFinite(n)) return "0";
    return n.toLocaleString("vi-VN");
  }

  function renderAccountsTable() {
    const tbody = document.getElementById("accountsTableBody");
    const summaryLbl = document.getElementById("tableSummaryLabel");
    if (!tbody) return;

    const nodes = state.summary.nodes || [];
    const syncedCount = (state.summary.ready_nodes || 0) + (state.summary.running_nodes || 0);
    const totalRemain = state.summary.total_tokens_remaining || 0;
    const totalQuota = state.summary.total_tokens_quota || 0;
    const totalUsed = state.summary.total_tokens_used || 0;
    const totalReqs = state.summary.total_requests || 0;

    // Cập nhật 4 thẻ giám sát Trạng thái Tài khoản & Pool Token ở đầu trang Admin
    const statAcc = document.getElementById("adminStatAccounts");
    const statAccSub = document.getElementById("adminStatAccountsSub");
    const statTokRem = document.getElementById("adminStatTokensRemain");
    const statTokQuotaSub = document.getElementById("adminStatTokensQuotaSub");
    const statTokUsed = document.getElementById("adminStatTokensUsed");
    const statReqSub = document.getElementById("adminStatRequestsSub");
    const statHealth = document.getElementById("adminStatTokenHealth");
    const statHealthSub = document.getElementById("adminStatTokenHealthSub");

    if (statAcc) {
      statAcc.textContent = syncedCount + " / " + nodes.length + " đang hoạt động";
      statAcc.style.color = syncedCount > 0 ? "#16a34a" : "#dc2626";
    }
    if (statAccSub) {
      statAccSub.textContent =
        nodes.length === 0
          ? "Chưa có tài khoản trong Pool"
          : (state.summary.running_nodes || 0) +
            " đang xử lý · " +
            (state.summary.ready_nodes || 0) +
            " sẵn sàng · " +
            (state.summary.unavailable_nodes || 0) +
            " ngắt kết nối";
    }
    if (statTokRem) {
      if (totalRemain >= 1000000000) {
        const bil = (totalRemain / 1000000000).toFixed(totalRemain % 1000000000 === 0 ? 0 : 2);
        statTokRem.textContent = "Còn " + bil + " tỷ token";
      } else {
        statTokRem.textContent = formatNumWithCommas(totalRemain) + " tokens";
      }
    }
    if (statTokQuotaSub) {
      statTokQuotaSub.textContent = "Không bao giờ hết hạn (Gộp Pool)";
    }
    if (statTokUsed) {
      statTokUsed.textContent = formatNumWithCommas(totalUsed) + " tokens";
    }
    if (statReqSub) {
      statReqSub.textContent = "Tổng lượt gọi: " + formatNumWithCommas(totalReqs) + " requests";
    }
    if (statHealth) {
      if (nodes.length === 0) {
        statHealth.textContent = "Chưa cắm Token";
        statHealth.style.color = "#737373";
      } else if (syncedCount === nodes.length) {
        statHealth.textContent = "100% Token Hợp Lệ";
        statHealth.style.color = "#16a34a";
      } else if (syncedCount > 0) {
        statHealth.textContent = syncedCount + "/" + nodes.length + " Token Sống";
        statHealth.style.color = "#d97706";
      } else {
        statHealth.textContent = "Cần làm mới Token";
        statHealth.style.color = "#dc2626";
      }
    }
    if (statHealthSub) {
      statHealthSub.textContent =
        "Chiến lược: " +
        (state.summary.routing_strategy || "least_active") +
        " · RAM: " +
        (state.summary.mem_alloc_mb || 1) +
        "MB";
    }

    if (summaryLbl) {
      summaryLbl.textContent =
        nodes.length +
        " tài khoản · " +
        syncedCount +
        " đang đồng bộ gộp Token";
    }

    if (nodes.length === 0) {
      tbody.innerHTML =
        '<div class="admin-gray-block" style="text-align:center;color:#737373;padding:28px;">' +
        'Chưa có tài khoản nào trong Pool Token. Hãy bấm <strong>"Công cụ kết nối (Tự bắt)"</strong> ở cột bên trái hoặc dán Cookie bên dưới.' +
        "</div>";
      return;
    }

    tbody.innerHTML = "";
    nodes.forEach(function (n) {
      const group = document.createElement("div");
      group.className = "admin-account-group";

      const isReady = n.status === "ready" || n.status === "running";
      const statusPill = isReady
        ? "<span style='display:inline-flex;align-items:center;gap:4px;padding:2px 9px;border-radius:999px;background:#dcfce7;color:#15803d;font-size:11.5px;font-weight:600;white-space:nowrap;'>● Đã đồng bộ (Gộp Token)</span>"
        : "<span style='display:inline-flex;align-items:center;gap:4px;padding:2px 9px;border-radius:999px;background:#fee2e2;color:#b91c1c;font-size:11.5px;font-weight:600;white-space:nowrap;'>● Ngắt kết nối</span>";

      const sessPill = n.has_hatch_sess
        ? "<span style='display:inline-flex;align-items:center;padding:2px 8px;border-radius:999px;background:#ecfdf5;color:#047857;border:1px solid #a7f3d0;font-size:11px;font-weight:600;white-space:nowrap;'>🔑 hatch_sess: Đang hoạt động</span>"
        : "<span style='display:inline-flex;align-items:center;padding:2px 8px;border-radius:999px;background:#fef2f2;color:#b91c1c;border:1px solid #fecaca;font-size:11px;font-weight:600;white-space:nowrap;'>⚠️ Thiếu hatch_sess</span>";

      const shieldPill = n.proxy_url
        ? "<span style='display:inline-flex;align-items:center;padding:2px 8px;border-radius:999px;background:#eff6ff;color:#1d4ed8;font-size:11px;white-space:nowrap;'>🌐 Proxy riêng</span>"
        : "<span style='display:inline-flex;align-items:center;padding:2px 8px;border-radius:999px;background:#f0fdf4;color:#15803d;font-size:11px;white-space:nowrap;'>🛡️ Chống trùng IP (Cô lập datr · Canvas · WebGL)</span>";

      const devProfText = n.device_profile || "Cô lập vân tay phần cứng riêng";

      // Dữ liệu 2 thanh Token chuẩn trang gốc:
      // Thanh 1: Gói miễn phí (Giới hạn hàng tuần)
      const freeLabel = n.free_plan_label || "Gói miễn phí";
      const freePct = typeof n.free_plan_percent === "number" ? Math.max(0, Math.min(100, n.free_plan_percent)) : 4;
      const freeValue = n.free_plan_value || ("Đã dùng " + freePct + "%");
      const freeSub = n.free_plan_subtitle || "Giới hạn hàng tuần sẽ đặt lại vào 10 tháng 11";

      // Thanh 2: Token bổ sung (Không bao giờ hết hạn — còn 1 tỷ token)
      const topupLabel = n.topup_label || "Token bổ sung";
      const topupPct = typeof n.topup_percent === "number" ? Math.max(0, Math.min(100, n.topup_percent)) : 0;
      const topupValue = n.topup_value || "Đã dùng 0% (còn 1 tỷ token)";
      const topupSub = n.topup_subtitle || "Không bao giờ hết hạn";

      const usedTok = n.tokens_used || 0;
      const reqCount = n.total_requests || 0;

      group.innerHTML =
        // 1. Thẻ Tài khoản Meta (Giống hộp trên trong ảnh Cài đặt gốc)
        '<div class="admin-meta-header-card">' +
        '  <div class="admin-meta-left">' +
        '    <div class="admin-meta-logo">' +
        '      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
        '        <path d="M18.178 8c5.096 0 5.096 8 0 8-5.095 0-7.133-8-12.739-8-4.585 0-4.585 8 0 8 5.606 0 7.644-8 12.74-8z"/>' +
        "      </svg>" +
        "    </div>" +
        '    <div style="flex:1;min-width:180px;">' +
        '      <div style="display:flex;align-items:center;gap:6px;flex-wrap:wrap;">' +
        '        <strong style="font-size:15px;color:#171717;">Tài khoản Meta · ' +
        escapeHtml(n.label) +
        "</strong>" +
        statusPill +
        sessPill +
        shieldPill +
        "      </div>" +
        '      <div style="font-size:12.5px;color:#737373;margin-top:4px;">' +
        "        Mật khẩu, bảo mật, thông tin cá nhân · Mã: <code>" +
        escapeHtml(n.fingerprint) +
        "</code> · 💻 " +
        escapeHtml(devProfText) +
        "      </div>" +
        "    </div>" +
        "  </div>" +
        '  <div class="admin-meta-actions">' +
        '    <button type="button" class="admin-action-pill-btn btn-wake-node" data-id="' +
        escapeHtml(n.id) +
        '">🔄 Đồng bộ Token</button>' +
        '    <button type="button" class="admin-action-pill-btn btn-open-native" data-id="' +
        escapeHtml(n.id) +
        '">🖥️ Mở gốc</button>' +
        '    <button type="button" class="admin-action-pill-btn btn-del-node" style="color:#dc2626;border-color:#fecaca;" data-id="' +
        escapeHtml(n.id) +
        '">Xóa</button>' +
        "  </div>" +
        "</div>" +
        // 2. Nhãn Mức sử dụng
        '<div class="admin-section-label" style="margin:2px 0 -4px 4px;">Mức sử dụng — ' +
        escapeHtml(n.label) +
        "</div>" +
        // 3. Khối Mức sử dụng gồm ĐỦ 2 THANH TOKEN giống hệt 100% ảnh gốc
        '<div class="admin-usage-box">' +
        // Thanh 1: Gói miễn phí
        '  <div class="admin-usage-row-top">' +
        '    <span class="admin-usage-title">' +
        escapeHtml(freeLabel) +
        "</span>" +
        '    <span class="admin-usage-right-val">' +
        escapeHtml(freeValue) +
        "</span>" +
        "  </div>" +
        '  <div class="admin-usage-sub">' +
        escapeHtml(freeSub) +
        "</div>" +
        '  <div class="admin-usage-bar-track">' +
        '    <div class="admin-usage-bar-fill" style="width:' +
        freePct +
        '%;"></div>' +
        "  </div>" +
        '  <div class="admin-usage-divider"></div>' +
        // Thanh 2: Token bổ sung (Còn 1 tỷ token - Không bao giờ hết hạn)
        '  <div class="admin-usage-row-top">' +
        '    <span class="admin-usage-title">' +
        escapeHtml(topupLabel) +
        "</span>" +
        '    <span class="admin-usage-right-val">' +
        escapeHtml(topupValue) +
        "</span>" +
        "  </div>" +
        '  <div class="admin-usage-sub">' +
        escapeHtml(topupSub) +
        "</div>" +
        '  <div class="admin-usage-bar-track">' +
        '    <div class="admin-usage-bar-fill" style="width:' +
        topupPct +
        '%;"></div>' +
        "  </div>" +
        '  <div class="admin-usage-divider" style="margin:14px 0 10px;"></div>' +
        // Chân thẻ: Nâng cấp + Thông số điều phối
        '  <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px;">' +
        '    <button type="button" class="btn-open-native" data-id="' +
        escapeHtml(n.id) +
        '" style="border:none;background:transparent;padding:0;color:#0066ff;font-family:var(--font-sans);font-size:14.5px;font-weight:500;cursor:pointer;">Nâng cấp</button>' +
        '    <div style="font-size:12px;color:#737373;display:flex;gap:12px;flex-wrap:wrap;">' +
        "      <span>📊 Đã dùng qua Router: <strong>" +
        formatNumWithCommas(usedTok) +
        " tok</strong></span>" +
        "      <span>💬 Lượt gọi: <strong>" +
        formatNumWithCommas(reqCount) +
        "</strong></span>" +
        "      <span>⚡ Độ trễ: <strong>" +
        (n.latency_ms || 0) +
        "ms</strong></span>" +
        "      <span>🕒 Dùng cuối: <strong>" +
        escapeHtml(n.last_used_at || "—") +
        "</strong></span>" +
        "    </div>" +
        "  </div>" +
        "</div>";

      tbody.appendChild(group);
    });

    tbody.querySelectorAll(".btn-open-native").forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const nodeId = btn.getAttribute("data-id");
        btn.disabled = true;
        try {
          const res = await apiRequest("/api/accounts/open-native", {
            method: "POST",
            body: { node_id: nodeId },
          });
          showToast(res.detail || "Đã mở cửa sổ trình duyệt Muse.ai chính gốc.");
        } catch (err) {
          showToast("Lỗi mở cửa sổ gốc: " + err.message);
        } finally {
          btn.disabled = false;
        }
      });
    });

    tbody.querySelectorAll(".btn-wake-node").forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const nodeId = btn.getAttribute("data-id");
        btn.disabled = true;
        btn.textContent = "⏳ Đang kiểm tra Token...";
        try {
          const res = await apiRequest("/api/accounts/wake", {
            method: "POST",
            body: { node_id: nodeId },
          });
          const tokMsg =
            res.tokens_remaining > 0
              ? " · Hạn mức: " + formatNumWithCommas(res.tokens_remaining) + " tokens"
              : "";
          showToast(
            "✅ " + (res.detail || "Đã kiểm tra & đồng bộ Token xong") + tokMsg
          );
          await refreshRouterState(true);
        } catch (err) {
          showToast("Lỗi kiểm tra Token: " + err.message);
        } finally {
          btn.disabled = false;
          btn.textContent = "🔄 Đồng bộ Token";
        }
      });
    });

    tbody.querySelectorAll(".btn-del-node").forEach(function (btn) {
      btn.addEventListener("click", async function () {
        const nodeId = btn.getAttribute("data-id");
        try {
          await apiRequest("/api/accounts/delete", {
            method: "POST",
            body: { node_id: nodeId },
          });
          showToast("Đã xóa tài khoản.");
          await refreshRouterState();
        } catch (err) {
          showToast("Lỗi xóa tài khoản: " + err.message);
        }
      });
    });
  }

  // ============================================================================
  // 4. ĐIỀU HƯỚNG & SỰ KIỆN TƯƠNG TÁC
  // ============================================================================

  // Tất cả panel IDs
  var ALL_PANELS = ["panelChat", "panelSearch", "panelDocs", "panelIdeas", "panelTasks", "panelLibrary", "panelAccounts"];

  function switchMainView(mode) {
    state.viewMode = mode;
    // Ẩn/hiện thanh rail bên trái khi vào Trang Cài Đặt Riêng (accounts)
    var leftRail = document.querySelector(".muse-left-rail");
    if (leftRail) {
      leftRail.classList.toggle("hidden-el", mode === "accounts");
    }

    // Đồng bộ URL sạch sẽ
    try {
      if (mode === "accounts") {
        if (window.location.pathname !== "/cai-dat" && window.location.pathname !== "/admin") {
          window.history.replaceState(null, "", "/cai-dat");
        }
      } else if (mode === "library") {
        if (window.location.pathname !== "/library/artifacts") {
          window.history.replaceState(null, "", "/library/artifacts");
        }
      } else if (window.location.pathname !== "/") {
        window.history.replaceState(null, "", "/");
      }
    } catch (_) {}

    // Ẩn tất cả panels
    ALL_PANELS.forEach(function(pid) {
      var el = document.getElementById(pid);
      if (el) el.classList.add("hidden-el");
    });
    // Hiện panel tương ứng
    var target = document.getElementById("panel" + mode.charAt(0).toUpperCase() + mode.slice(1));
    if (target) target.classList.remove("hidden-el");

    // Cập nhật trạng thái active cho rail buttons có data-view
    document.querySelectorAll(".rail-icon-btn[data-view]").forEach(function (b) {
      b.classList.toggle("active", b.getAttribute("data-view") === mode);
    });
    // Cập nhật active cho rail buttons không có data-view (search/docs/ideas/tasks)
    ["railBtnSearch","railBtnDocs","railBtnIdeas","railBtnTasks"].forEach(function(id) {
      var btn = document.getElementById(id);
      if (!btn) return;
      var map = { railBtnSearch:"search", railBtnDocs:"docs", railBtnIdeas:"ideas", railBtnTasks:"tasks" };
      btn.classList.toggle("active", map[id] === mode);
    });

    // Gọi loader theo mode
    if (mode === "library") {
      loadLibraryArtifacts();
    } else if (mode === "accounts") {
      silentAutoSyncAccounts();
      refreshLibSQLStatus();
      var studioCard = document.getElementById("libsqlStudioCard");
      var sqlInput = document.getElementById("libsqlSqlInput");
      if (studioCard && !studioCard.classList.contains("hidden-el") && sqlInput && sqlInput.value.trim()) {
        executeAndRenderLibSQLQuery(sqlInput.value.trim());
      }
      if (
        !state.autoOpenedInlineChromeOnce &&
        state.summary &&
        (state.summary.ready_nodes || 0) === 0
      ) {
        state.autoOpenedInlineChromeOnce = true;
        setTimeout(function () {
          openRemoteBrowserLoginModal(document.getElementById("btnExtAutoLoginNew"));
        }, 150);
      }
    } else if (mode === "search") {
      var si = document.getElementById("searchInput");
      if (si) si.focus();
    } else if (mode === "docs") {
      loadDocsList();
    } else if (mode === "ideas") {
      loadMemoriesList();
    } else if (mode === "tasks") {
      loadTasksList("all");
    }
  }

  function switchRightSidebarTab(targetTab) {
    const rightSide = document.getElementById("museRightSidebar");
    if (rightSide) {
      rightSide.classList.remove("hidden-el");
    }
    document.querySelectorAll(".four-tab-btn[data-side-tab]").forEach(function (btn) {
      btn.classList.toggle("active", btn.getAttribute("data-side-tab") === targetTab);
    });
    const panes = {
      history: document.getElementById("sidePaneHistory"),
      nodes: document.getElementById("sidePaneNodes"),
      clock: document.getElementById("sidePaneClock"),
      plug: document.getElementById("sidePanePlug"),
    };
    Object.keys(panes).forEach(function (k) {
      if (panes[k]) {
        panes[k].classList.toggle("hidden-el", k !== targetTab);
      }
    });
  }

  async function fetchAndShowOAuthLoginLink(btnEl) {
    const origText = btnEl ? btnEl.textContent : "";
    if (btnEl) {
      btnEl.disabled = true;
      btnEl.textContent = "⏳ Đang lấy Link Đăng Nhập từ muse.ai...";
    }
    try {
      const res = await apiRequest("/api/accounts/oauth-link", { method: "POST" });
      if (!res || !res.login_url) {
        throw new Error(res.error || "Không nhận được link đăng nhập.");
      }
      if (!isLocalHost()) {
        // Trên máy chủ từ xa: link do server tạo mang cookie PKCE của server nên trình duyệt người dùng
        // đăng nhập sẽ bị Meta từ chối. Dùng điểm bắt đầu chính thức để trình duyệt tự giữ phiên của nó.
        res.login_url = "https://muse.ai/api/oidc/start?return_to=%2F";
      }
      state.lastOAuthSessionId = res.session_id || "";
      state.lastOAuthLoginUrl = res.login_url || "";

      const boxMain = document.getElementById("oauthLinkBox");
      const inpMain = document.getElementById("oauthLinkInput");
      if (boxMain && inpMain) {
        boxMain.classList.remove("hidden-el");
        inpMain.value = res.login_url;
      }

      const boxSide = document.getElementById("sideOauthLinkBox");
      const inpSide = document.getElementById("sideOauthLinkInput");
      if (boxSide && inpSide) {
        boxSide.classList.remove("hidden-el");
        inpSide.value = res.login_url;
      }

      try {
        await navigator.clipboard.writeText(res.login_url);
        showToast("🔗 Đã tự động lấy & sao chép Link Đăng Nhập OAuth vào bộ nhớ tạm!");
      } catch (_) {
        showToast("🔗 Đã tự động lấy Link Đăng Nhập OAuth thành công!");
      }
      return res;
    } catch (err) {
      showToast("Lỗi lấy Link Đăng Nhập: " + err.message);
      return null;
    } finally {
      if (btnEl) {
        btnEl.disabled = false;
        btnEl.textContent = origText;
      }
    }
  }

  function isLocalHost() {
    const h = window.location.hostname;
    return h === "127.0.0.1" || h === "localhost" || h === "::1" || h === "[::1]";
  }

  function looksLikeOAuthCallback(text) {
    return /^https:\/\/(muse\.ai\/(api\/)?oidc\/callback|auth\.muse\.ai\/oidc\/muse)/.test(
      (text || "").trim()
    );
  }

  // Luồng 1 nút cho bản chạy trên máy chủ từ xa (Render): không có trình duyệt Playwright,
  // nên mở trang đăng nhập Meta ở tab mới rồi tự đọc URL callback từ bộ nhớ tạm khi người dùng quay lại.
  async function cloudOAuthLoginFlow(btnEl) {
    const origText = btnEl ? btnEl.textContent : "";
    const popup = window.open("about:blank", "_blank");
    if (btnEl) {
      btnEl.disabled = true;
      btnEl.textContent = "⏳ Đang tạo link đăng nhập...";
    }
    const restore = function () {
      if (btnEl) {
        btnEl.disabled = false;
        btnEl.textContent = origText;
      }
    };

    const res = await fetchAndShowOAuthLoginLink(null);
    if (!res || !res.login_url) {
      if (popup) popup.close();
      restore();
      return;
    }
    if (popup) {
      popup.location.href = res.login_url;
    } else {
      showToast("Trình duyệt chặn cửa sổ mới — hãy cho phép popup hoặc dán Link Đăng Nhập vào tab khác.");
    }

    // Sau khi đăng nhập Meta, trình duyệt của người dùng tự hoàn tất phiên với muse.ai và vào thẳng
    // giao diện chat (URL callback không còn trên thanh địa chỉ). Cookie hatch_sess là HttpOnly nên
    // JavaScript không đọc được — chỉ có thể lấy qua DevTools. Chuyển nút sang chế độ "dán từ clipboard".
    state.cloudLoginWaiting = true;
    if (btnEl) {
      btnEl.disabled = false;
      btnEl.textContent = "📋 Đã đăng nhập xong? Copy hatch_sess (F12 → Application → Cookies → muse.ai) rồi bấm vào đây";
    }
    showToast(
      "Đăng nhập Meta ở tab vừa mở cho tới khi vào được giao diện muse.ai → F12 → Application → Cookies → muse.ai → bấm đúp giá trị hatch_sess → Ctrl+C. Rồi quay lại đây bấm nút đen."
    );
  }

  async function submitCloudLoginFromClipboard(btnEl) {
    let text = "";
    try {
      text = (await navigator.clipboard.readText()).trim();
    } catch (_) {
      showToast("Không đọc được bộ nhớ tạm — hãy cho phép quyền clipboard hoặc dán hatch_sess vào ô bên trên rồi bấm 'Xác thực & Thêm tài khoản'.");
      return;
    }
    if (!text) {
      showToast("Bộ nhớ tạm chưa có giá trị hatch_sess — hãy copy lại từ DevTools.");
      return;
    }
    if (/^https?:\/\//i.test(text) && !looksLikeOAuthCallback(text)) {
      showToast("Bộ nhớ tạm đang là một đường link, không phải cookie hatch_sess.");
      return;
    }
    state.cloudLoginWaiting = false;
    const labelEl = document.getElementById("accLabelInput");
    const proxyEl = document.getElementById("accProxyInput") || document.getElementById("quickProxyInput");
    await submitCookieCredential(
      labelEl ? labelEl.value.trim() : "",
      "cookie",
      "https://muse.ai",
      text,
      btnEl,
      proxyEl ? proxyEl.value.trim() : ""
    );
    if (btnEl) {
      btnEl.textContent = "🔌 Xác thực & Thêm tài khoản";
    }
  }

  const MART_EXTENSION_ID = "mjlkhmjhbhkjfccffeoimoindodmglaj";
  var extReqCounter = 0;
  var pendingExtResolvers = {};
  var autoSyncCookieInFlight = false;

  function sendViaDirectExternal(payload) {
    return new Promise(function (resolve) {
      try {
        if (
          typeof window.chrome === "undefined" ||
          !window.chrome.runtime ||
          typeof window.chrome.runtime.sendMessage !== "function"
        ) {
          resolve(null);
          return;
        }
        window.chrome.runtime.sendMessage(MART_EXTENSION_ID, payload, function (resp) {
          if (window.chrome.runtime.lastError) {
            resolve(null);
            return;
          }
          resolve(resp || null);
        });
      } catch (_) {
        resolve(null);
      }
    });
  }

  function sendExtensionMessage(payload) {
    var act = (payload && payload.action) || "";
    var timeoutMs = act === "ping" || act === "ping_with_cookie" ? 450 : 95000;

    return new Promise(function (resolve) {
      var reqId = "mext_" + Date.now() + "_" + ++extReqCounter;
      var settled = false;
      var timer = setTimeout(function () {
        if (settled) return;
        delete pendingExtResolvers[reqId];
        // Dự phòng qua externally_connectable nếu tab chưa nạp content.js
        sendViaDirectExternal(payload).then(function (fallbackResp) {
          if (!settled) {
            settled = true;
            resolve(fallbackResp);
          }
        });
      }, timeoutMs);

      pendingExtResolvers[reqId] = function (resp) {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        delete pendingExtResolvers[reqId];
        if (resp) {
          resolve(resp);
        } else {
          sendViaDirectExternal(payload).then(resolve);
        }
      };

      try {
        window.postMessage(
          {
            __mart_ext_req: true,
            reqId: reqId,
            payload: payload,
          },
          window.location.origin || "*"
        );
      } catch (_) {
        clearTimeout(timer);
        delete pendingExtResolvers[reqId];
        sendViaDirectExternal(payload).then(resolve);
      }
    });
  }

  async function autoIngestCookieFromExtension(cookieHeader) {
    if (!cookieHeader || cookieHeader.indexOf("hatch_sess=") < 0) return;
    if (autoSyncCookieInFlight) return;
    if (state.lastAutoSyncedCookie === cookieHeader) return;

    // Tránh tự ghi đè nếu đã có tài khoản hoạt động, trừ khi Pool đang trống (0 tài khoản)
    var curNodes = (state.summary && state.summary.nodes) || [];
    if (curNodes.length > 0 && state.lastAutoSyncedOnce) {
      return;
    }

    autoSyncCookieInFlight = true;
    state.lastAutoSyncedCookie = cookieHeader;
    try {
      const node = await apiRequest("/api/accounts/add", {
        method: "POST",
        body: {
          label: "",
          plan_tier: "Muse.ai",
          auth_type: "cookie",
          upstream_url: "https://muse.ai",
          proxy_url: "",
          credential: cookieHeader,
          tokens_remaining: 0,
        },
      });
      state.lastAutoSyncedOnce = true;
      showToast("✅ Đã tự động bắt & kết nối " + (node.label || "tài khoản Muse.ai") + " thành công!");
      await refreshRouterState(true);
      renderActiveThreadMessages();
    } catch (_) {
      state.lastAutoSyncedCookie = "";
    } finally {
      autoSyncCookieInFlight = false;
    }
  }

  window.addEventListener("message", function (ev) {
    if (ev.source !== window || !ev.data) return;
    if (ev.data.__mart_ext_resp === true && ev.data.reqId) {
      var fn = pendingExtResolvers[ev.data.reqId];
      if (typeof fn === "function") {
        fn(ev.data.result || null);
      }
      return;
    }
    if (ev.data.__mart_ext_push === true && ev.data.event === "MART_EXT_SESSION_READY") {
      state.hasMartExtension = true;
      var modal = document.getElementById("martCloudExtModal");
      if (modal && modal.parentNode) modal.remove();
      refreshCloudExtensionBanner();
      if (ev.data.cookie && ev.data.cookie.indexOf("hatch_sess=") >= 0) {
        autoIngestCookieFromExtension(ev.data.cookie);
      }
    }
  });

  var activeInlineChromeCleanup = null;

  async function refreshCloudExtensionBanner() {
    const banner = document.getElementById("cloudExtBanner");
    if (!banner) return false;
    banner.classList.remove("hidden-el");
    return false;
  }

  async function openRemoteBrowserLoginModal(btnEl, proxyUrlOverride) {
    if (state.viewMode !== "accounts") {
      state.autoOpenedInlineChromeOnce = true;
      switchMainView("accounts");
    }

    if (typeof activeInlineChromeCleanup === "function") {
      activeInlineChromeCleanup(true);
    }

    const banner = document.getElementById("cloudExtBanner");
    const idleHint = document.getElementById("inlineChromeIdleHint");
    const chromeBody = document.getElementById("inlineChromeBody");
    const urlBar = document.getElementById("inlineChromeUrlBar");
    const statusOverlay = document.getElementById("inlineChromeStatusOverlay");
    const screenImg = document.getElementById("inlineChromeScreenImg");
    const viewportWrap = document.getElementById("inlineChromeViewportWrap");
    const quickInput = document.getElementById("inlineChromeQuickText");
    const btnStartMain = document.getElementById("btnExtAutoLoginNew");
    const btnRestart = document.getElementById("btnInlineChromeRestart");
    const btnReload = document.getElementById("btnInlineChromeReload");
    const btnExpand = document.getElementById("btnInlineChromeExpand");
    const btnStop = document.getElementById("btnInlineChromeStop");
    const btnSendQuick = document.getElementById("btnInlineSendQuickText");
    const btnSendEnter = document.getElementById("btnInlineSendEnter");
    const btnClear = document.getElementById("btnInlineClearField");

    if (!banner || !chromeBody) return;

    const labelEl = document.getElementById("accLabelInput");
    const proxyEl = document.getElementById("accProxyInput") || document.getElementById("quickProxyInput");
    const labelVal = labelEl ? labelEl.value.trim() : "";
    const proxyVal =
      typeof proxyUrlOverride === "string"
        ? proxyUrlOverride
        : proxyEl
        ? proxyEl.value.trim()
        : "";

    const origBannerStyle = banner.getAttribute("style") || "";
    if (idleHint) idleHint.classList.add("hidden-el");
    chromeBody.classList.remove("hidden-el");
    if (btnStartMain) btnStartMain.classList.add("hidden-el");
    [btnRestart, btnReload, btnExpand, btnStop].forEach(function (b) {
      if (b) b.classList.remove("hidden-el");
    });
    if (screenImg) {
      screenImg.style.display = "none";
      screenImg.removeAttribute("src");
    }
    if (statusOverlay) statusOverlay.style.display = "block";
    if (urlBar) urlBar.value = "Đang khởi động trình duyệt Chrome đăng nhập...";

    setTimeout(function () {
      banner.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }, 60);

    let sessionId = "";
    let closed = false;
    let isExpanded = false;
    let isChromeFocused = true;
    let pollTimer = null;
    let pollBusy = false;
    let inputChain = Promise.resolve();
    let virtualWidth = 1100;
    let virtualHeight = 680;

    function setExpandedMode(expand) {
      isExpanded = !!expand;
      if (isExpanded) {
        banner.style.cssText =
          "position:fixed;inset:12px;z-index:10000;border:1px solid #cbd5e1;border-radius:16px;background:#ffffff;display:flex;flex-direction:column;overflow:hidden;box-shadow:0 25px 60px rgba(0,0,0,0.42);";
        chromeBody.style.cssText = "display:flex;flex-direction:column;flex:1;min-height:0;";
        if (viewportWrap) {
          viewportWrap.style.flex = "1";
          viewportWrap.style.aspectRatio = "auto";
          viewportWrap.style.minHeight = "0";
        }
        if (btnExpand) btnExpand.textContent = "🗗 Thu về trang";
      } else {
        banner.setAttribute("style", origBannerStyle);
        chromeBody.removeAttribute("style");
        if (viewportWrap) {
          viewportWrap.style.flex = "";
          viewportWrap.style.aspectRatio = "1100/680";
          viewportWrap.style.minHeight = "";
        }
        if (btnExpand) btnExpand.textContent = "⛶ Phóng rộng";
      }
    }

    function cleanupInlineChrome(shouldCancelBackend) {
      if (closed) return;
      closed = true;
      activeInlineChromeCleanup = null;
      if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
      }
      window.removeEventListener("keydown", onWindowKeyDown, true);
      window.removeEventListener("paste", onWindowPaste, true);
      document.removeEventListener("mousedown", onDocMouseDown, true);
      if (screenImg) {
        screenImg.onclick = null;
        screenImg.onwheel = null;
      }
      if (isExpanded) {
        setExpandedMode(false);
      }
      if (shouldCancelBackend && sessionId) {
        apiRequest("/api/accounts/remote-login/cancel", {
          method: "POST",
          body: { session_id: sessionId },
        }).catch(function () {});
      }
      chromeBody.classList.add("hidden-el");
      if (idleHint) idleHint.classList.remove("hidden-el");
      if (btnStartMain) {
        btnStartMain.classList.remove("hidden-el");
        btnStartMain.disabled = false;
      }
      [btnRestart, btnReload, btnExpand, btnStop].forEach(function (b) {
        if (b) b.classList.add("hidden-el");
      });
      if (urlBar) urlBar.value = "https://muse.ai/api/oidc/start?return_to=%2F";
    }

    activeInlineChromeCleanup = cleanupInlineChrome;

    async function handleCompletedNode(nodeObj) {
      cleanupInlineChrome(false);
      const savedLabel = (nodeObj && nodeObj.label) || "tài khoản Muse";
      showToast("✅ Đã tự động chặn bắt & kích hoạt " + savedLabel + " thành công!");
      await refreshRouterState(true);
      renderActiveThreadMessages();
      const topSection = document.getElementById("adminUsageSection");
      if (topSection) {
        topSection.scrollIntoView({ behavior: "smooth", block: "start" });
      }
    }

    function applySnapshot(snap) {
      if (!snap || closed) return;
      if (snap.width) virtualWidth = snap.width;
      if (snap.height) virtualHeight = snap.height;
      if (snap.url && urlBar) {
        urlBar.value = snap.url;
      }
      if (snap.frame && screenImg) {
        screenImg.src = snap.frame;
        screenImg.style.display = "block";
        if (statusOverlay) statusOverlay.style.display = "none";
      }
      if (snap.status === "completed") {
        handleCompletedNode(snap.node);
        return;
      }
      if (snap.status === "error" || snap.status === "cancelled") {
        const msg = snap.error || "Phiên đăng nhập đã kết thúc.";
        showToast("Đăng nhập chưa hoàn tất: " + msg);
        cleanupInlineChrome(false);
      }
    }

    function enqueueRemoteInput(payload) {
      if (!sessionId || closed) return;
      inputChain = inputChain
        .then(function () {
          if (!sessionId || closed) return null;
          const bodyObj = Object.assign({ session_id: sessionId }, payload);
          return apiRequest("/api/accounts/remote-login/input", {
            method: "POST",
            body: bodyObj,
          });
        })
        .then(function (snap) {
          if (snap) applySnapshot(snap);
        })
        .catch(function () {});
    }

    function computeImageCoords(clientX, clientY) {
      if (!screenImg) return { x: 550, y: 340 };
      const rect = screenImg.getBoundingClientRect();
      if (!rect.width || !rect.height) return { x: 550, y: 340 };
      const containerRatio = rect.width / rect.height;
      const targetRatio = virtualWidth / virtualHeight;
      let renderW = rect.width;
      let renderH = rect.height;
      let offsetX = 0;
      let offsetY = 0;
      if (containerRatio > targetRatio) {
        renderW = rect.height * targetRatio;
        offsetX = (rect.width - renderW) / 2;
      } else {
        renderH = rect.width / targetRatio;
        offsetY = (rect.height - renderH) / 2;
      }
      const relX = Math.max(0, Math.min(renderW, clientX - rect.left - offsetX));
      const relY = Math.max(0, Math.min(renderH, clientY - rect.top - offsetY));
      return {
        x: Math.round((relX / renderW) * virtualWidth),
        y: Math.round((relY / renderH) * virtualHeight),
      };
    }

    function showClickRipple(clientX, clientY) {
      if (!viewportWrap) return;
      const wRect = viewportWrap.getBoundingClientRect();
      const dot = document.createElement("span");
      dot.style.cssText =
        "position:absolute;width:18px;height:18px;border-radius:50%;border:2px solid #38bdf8;background:rgba(56,189,248,0.28);pointer-events:none;transform:translate(-50%,-50%);transition:opacity 0.35s ease;";
      dot.style.left = clientX - wRect.left + "px";
      dot.style.top = clientY - wRect.top + "px";
      viewportWrap.appendChild(dot);
      setTimeout(function () {
        dot.style.opacity = "0";
      }, 40);
      setTimeout(function () {
        if (dot.parentNode) dot.remove();
      }, 400);
    }

    function onDocMouseDown(ev) {
      if (!banner) return;
      isChromeFocused = banner.contains(ev.target);
    }
    document.addEventListener("mousedown", onDocMouseDown, true);

    if (screenImg) {
      screenImg.onclick = function (ev) {
        isChromeFocused = true;
        if (quickInput && document.activeElement === quickInput) {
          quickInput.blur();
        }
        if (viewportWrap) viewportWrap.focus({ preventScroll: true });
        const pt = computeImageCoords(ev.clientX, ev.clientY);
        showClickRipple(ev.clientX, ev.clientY);
        enqueueRemoteInput({ type: "click", x: pt.x, y: pt.y });
      };

      let lastWheelTs = 0;
      screenImg.onwheel = function (ev) {
        if (!isExpanded && !isChromeFocused) return;
        ev.preventDefault();
        const now = Date.now();
        if (now - lastWheelTs < 110) return;
        lastWheelTs = now;
        const pt = computeImageCoords(ev.clientX, ev.clientY);
        enqueueRemoteInput({
          type: "scroll",
          x: pt.x,
          y: pt.y,
          delta_x: ev.deltaX || 0,
          delta_y: ev.deltaY || 0,
        });
      };
    }

    function isOtherInputFocused() {
      const ae = document.activeElement;
      if (!ae) return false;
      if (ae === viewportWrap) return false;
      const tag = (ae.tagName || "").toUpperCase();
      return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || ae.isContentEditable;
    }

    function onWindowPaste(ev) {
      if (closed || (!isChromeFocused && !isExpanded) || isOtherInputFocused()) return;
      const pasted = ev.clipboardData ? ev.clipboardData.getData("text/plain") : "";
      if (pasted) {
        ev.preventDefault();
        ev.stopImmediatePropagation();
        enqueueRemoteInput({ type: "text", text: pasted });
      }
    }

    function onWindowKeyDown(ev) {
      if (closed || (!isChromeFocused && !isExpanded) || isOtherInputFocused()) return;
      if ((ev.ctrlKey || ev.metaKey) && (ev.key === "v" || ev.key === "V")) {
        return; // Để sự kiện paste xử lý nội dung clipboard
      }
      if ((ev.ctrlKey || ev.metaKey) && (ev.key === "a" || ev.key === "A" || ev.key === "Backspace")) {
        ev.preventDefault();
        ev.stopImmediatePropagation();
        enqueueRemoteInput({ type: "clear" });
        return;
      }
      const specialKeys = [
        "Enter",
        "Backspace",
        "Tab",
        "Escape",
        "Delete",
        "ArrowLeft",
        "ArrowRight",
        "ArrowUp",
        "ArrowDown",
      ];
      if (specialKeys.indexOf(ev.key) >= 0) {
        if (ev.key === "Escape" && isExpanded) {
          ev.preventDefault();
          ev.stopImmediatePropagation();
          setExpandedMode(false);
          return;
        }
        ev.preventDefault();
        ev.stopImmediatePropagation();
        enqueueRemoteInput({ type: "key", key: ev.key });
        return;
      }
      if (ev.key && ev.key.length === 1 && !ev.ctrlKey && !ev.metaKey && !ev.altKey) {
        ev.preventDefault();
        ev.stopImmediatePropagation();
        enqueueRemoteInput({ type: "text", text: ev.key });
      }
    }

    window.addEventListener("paste", onWindowPaste, true);
    window.addEventListener("keydown", onWindowKeyDown, true);

    if (btnStop) {
      btnStop.onclick = function () {
        cleanupInlineChrome(true);
      };
    }
    if (btnExpand) {
      btnExpand.onclick = function () {
        setExpandedMode(!isExpanded);
      };
    }
    if (btnReload) {
      btnReload.onclick = function () {
        enqueueRemoteInput({ type: "reload" });
      };
    }
    if (btnRestart) {
      btnRestart.onclick = function () {
        enqueueRemoteInput({ type: "restart_login" });
      };
    }
    function flushQuickText(alsoPressEnter) {
      if (!quickInput) return;
      const val = quickInput.value;
      if (val) {
        enqueueRemoteInput({ type: "text", text: val });
        quickInput.value = "";
      }
      if (alsoPressEnter) {
        enqueueRemoteInput({ type: "key", key: "Enter" });
      }
      quickInput.blur();
      isChromeFocused = true;
      if (viewportWrap) viewportWrap.focus({ preventScroll: true });
    }
    if (btnSendQuick) {
      btnSendQuick.onclick = function () {
        flushQuickText(false);
      };
    }
    if (quickInput) {
      quickInput.onkeydown = function (ev) {
        if (ev.key === "Enter") {
          ev.preventDefault();
          flushQuickText(true);
        }
      };
    }
    if (btnSendEnter) {
      btnSendEnter.onclick = function () {
        isChromeFocused = true;
        enqueueRemoteInput({ type: "key", key: "Enter" });
      };
    }
    if (btnClear) {
      btnClear.onclick = function () {
        isChromeFocused = true;
        enqueueRemoteInput({ type: "clear" });
      };
    }

    try {
      const startRes = await apiRequest("/api/accounts/remote-login/start", {
        method: "POST",
        body: {
          label: labelVal,
          proxy_url: proxyVal,
        },
      });
      if (closed) {
        if (startRes && startRes.session_id) {
          apiRequest("/api/accounts/remote-login/cancel", {
            method: "POST",
            body: { session_id: startRes.session_id },
          }).catch(function () {});
        }
        return;
      }
      sessionId = startRes.session_id || "";
      if (startRes.width) virtualWidth = startRes.width;
      if (startRes.height) virtualHeight = startRes.height;

      pollTimer = setInterval(async function () {
        if (closed || !sessionId || pollBusy) return;
        pollBusy = true;
        try {
          const snap = await apiRequest(
            "/api/accounts/remote-login/state?session_id=" + encodeURIComponent(sessionId)
          );
          applySnapshot(snap);
        } catch (_) {
          // Bỏ qua lỗi mạng thoáng qua
        } finally {
          pollBusy = false;
        }
      }, 550);
    } catch (startErr) {
      showToast("Không thể khởi động trang Chrome đăng nhập: " + startErr.message);
      cleanupInlineChrome(false);
    }
  }

  async function executeBrowserRelayIfNeeded(res, onProgress) {
    if (!res || !res.browser_relay) return res;

    const ping = await sendExtensionMessage({ action: "ping" });
    if (!ping || !ping.ok || !ping.supportsChatRelay) {
      const cancelRes = await apiRequest("/api/chat/relay-complete", {
        method: "POST",
        body: {
          thread_id: res.thread_id || "",
          turn_id: res.turn_id || "",
          node_id: res.node_id || "",
          model: res.model || "",
          error: "SERVER_BROWSER_UNAVAILABLE",
        },
      }).catch(function () {
        return null;
      });
      if (cancelRes && cancelRes.summary) {
        state.summary = cancelRes.summary;
        updateMuseSidebarStatus();
        renderSideNodesList();
        renderAccountsTable();
      }
      throw new Error(
        "Trình duyệt ngầm trên máy chủ đang khởi tạo — vui lòng đợi vài giây rồi gửi lại tin nhắn."
      );
    }

    if (typeof onProgress === "function") {
      onProgress(45, "Đang nhận phản hồi từ " + (res.routed_node || "Muse") + "...");
    }

    const relayOut = await sendExtensionMessage({
      action: "execute_muse_chat",
      prompt: res.prompt || "",
      cookie: res.relay_cookie || "",
    });

    if (!relayOut || !relayOut.ok || !relayOut.content) {
      const errMsg = (relayOut && relayOut.error) || "Không nhận được phản hồi từ phiên Muse.ai.";
      const cancelRes = await apiRequest("/api/chat/relay-complete", {
        method: "POST",
        body: {
          thread_id: res.thread_id || "",
          turn_id: res.turn_id || "",
          node_id: res.node_id || "",
          model: res.model || "",
          error: errMsg,
        },
      }).catch(function () {
        return null;
      });
      if (cancelRes && cancelRes.summary) {
        state.summary = cancelRes.summary;
        updateMuseSidebarStatus();
        renderSideNodesList();
        renderAccountsTable();
      }
      throw new Error(errMsg);
    }

    return await apiRequest("/api/chat/relay-complete", {
      method: "POST",
      body: {
        thread_id: res.thread_id || "",
        turn_id: res.turn_id || "",
        node_id: res.node_id || "",
        model: res.model || "",
        reply: relayOut.content,
        tokens_used: relayOut.tokens_used || 0,
        latency_ms: relayOut.latency_ms || 1000,
      },
    });
  }

  async function triggerInteractiveBrowserLogin(btnEl, sessionIdOverride, proxyUrlOverride) {
    if (!isLocalHost() || state.viewMode === "accounts") {
      await openRemoteBrowserLoginModal(btnEl, proxyUrlOverride);
      return;
    }
    const origText = btnEl ? btnEl.textContent : "";
    if (btnEl) {
      btnEl.disabled = true;
      btnEl.textContent = "⏳ Đang đợi đăng nhập trên cửa sổ Muse.ai...";
    }
    try {
      const labelEl = document.getElementById("accLabelInput");
      const proxyEl = document.getElementById("accProxyInput") || document.getElementById("quickProxyInput");
      const proxyVal =
        typeof proxyUrlOverride === "string"
          ? proxyUrlOverride
          : proxyEl
          ? proxyEl.value.trim()
          : "";
      const node = await apiRequest("/api/accounts/browser-login", {
        method: "POST",
        body: {
          label: labelEl ? labelEl.value.trim() : "",
          tokens_remaining: 0,
          session_id: sessionIdOverride || state.lastOAuthSessionId || "",
          proxy_url: proxyVal,
        },
      });
      showToast("✅ Đã bắt Cookie thật & đồng bộ thành công " + node.label + "!");
      await refreshRouterState();
      renderActiveThreadMessages();
    } catch (err) {
      if (btnEl) {
        btnEl.disabled = false;
        btnEl.textContent = origText;
      }
      if (err && /AI Bridge|18089|connection refused|connectex/i.test(err.message || "")) {
        await openRemoteBrowserLoginModal(btnEl, proxyUrlOverride);
        return;
      }
      showToast("Đăng nhập trình duyệt chưa hoàn tất: " + err.message);
    } finally {
      if (btnEl && !activeInlineChromeCleanup) {
        btnEl.disabled = false;
        btnEl.textContent = origText;
      }
    }
  }

  async function submitCookieCredential(label, authType, upstreamUrl, rawCred, btnEl, proxyUrl) {
    if (!rawCred || !rawCred.trim()) {
      // Nếu người dùng để trống ô Cookie mà bấm nút xác thực, tự động mở cửa sổ trình duyệt đăng nhập Muse.ai
      await triggerInteractiveBrowserLogin(btnEl, "", proxyUrl || "");
      return;
    }

    const origText = btnEl ? btnEl.textContent : "";
    if (/^https:\/\/auth\.meta\.com\//i.test(rawCred.trim())) {
      showToast(
        "Đây là Link Đăng Nhập, không phải cookie. Hãy mở link này để đăng nhập, rồi copy giá trị hatch_sess (F12 → Application → Cookies → muse.ai) dán vào đây."
      );
      return;
    }
    if (btnEl) {
      btnEl.disabled = true;
      btnEl.textContent = "⏳ Đang xác thực với muse.ai...";
    }
    try {
      const lines = rawCred
        .split(/\r?\n/)
        .map(function (s) {
          return s.trim();
        })
        .filter(Boolean);

      if (lines.length > 1 && !rawCred.startsWith("[") && !rawCred.startsWith("{")) {
        const res = await apiRequest("/api/accounts/batch", {
          method: "POST",
          body: {
            lines: lines,
            plan_tier: "Muse.ai",
            auth_type: authType,
            upstream_url: upstreamUrl,
            proxy_url: proxyUrl || "",
            tokens_per_account: 0,
          },
        });
        showToast(
          "Đã thêm " +
            res.added +
            " tài khoản (" +
            res.verified_ready +
            "/" +
            res.added +
            " đồng bộ thành công)!"
        );
      } else {
        const node = await apiRequest("/api/accounts/add", {
          method: "POST",
          body: {
            label: label,
            plan_tier: "Muse.ai",
            auth_type: authType,
            upstream_url: upstreamUrl,
            proxy_url: proxyUrl || "",
            credential: rawCred,
            tokens_remaining: 0,
          },
        });
        if (node.status === "ready") {
          showToast("✅ Đã đồng bộ thành công " + node.label + " vào Pool Token!");
        } else {
          showToast(
            "⚠️ Đã lưu " + node.label + " nhưng chưa xác thực được: " + (node.status_detail || "")
          );
        }
      }
      await refreshRouterState();
      renderActiveThreadMessages();
    } catch (err) {
      showToast("Lỗi khi thêm tài khoản: " + err.message);
    } finally {
      if (btnEl) {
        btnEl.disabled = false;
        btnEl.textContent = origText;
      }
    }
  }

  function setupEventListeners() {
    // 1. Các icon trên thanh dọc bên trái
    document.querySelectorAll(".rail-icon-btn[data-view]").forEach(function (btn) {
      btn.addEventListener("click", function () {
        switchMainView(btn.getAttribute("data-view"));
      });
    });

    const railBtnSearch = document.getElementById("railBtnSearch");
    if (railBtnSearch) {
      railBtnSearch.addEventListener("click", function () {
        switchMainView("search");
      });
    }

    const railBtnDocs = document.getElementById("railBtnDocs");
    if (railBtnDocs) {
      railBtnDocs.addEventListener("click", function () {
        switchMainView("docs");
      });
    }

    const railBtnIdeas = document.getElementById("railBtnIdeas");
    if (railBtnIdeas) {
      railBtnIdeas.addEventListener("click", function () {
        switchMainView("ideas");
      });
    }

    const railBtnTasks = document.getElementById("railBtnTasks");
    if (railBtnTasks) {
      railBtnTasks.addEventListener("click", function () {
        switchMainView("tasks");
      });
    }

    const railBtnToggleSide = document.getElementById("railBtnToggleSide");
    if (railBtnToggleSide) {
      railBtnToggleSide.addEventListener("click", function () {
        const rightSide = document.getElementById("museRightSidebar");
        if (rightSide) rightSide.classList.toggle("hidden-el");
      });
    }

    // 2. Nút đóng cột phải (✕) & nút bút chì trên Avatar Muse (✎)
    const btnCloseRight = document.getElementById("btnCloseRightSidebar");
    if (btnCloseRight) {
      btnCloseRight.addEventListener("click", function () {
        const rightSide = document.getElementById("museRightSidebar");
        if (rightSide) rightSide.classList.add("hidden-el");
      });
    }

    const btnAvatarEdit = document.getElementById("btnAvatarManageAccounts");
    if (btnAvatarEdit) {
      btnAvatarEdit.addEventListener("click", function () {
        switchRightSidebarTab("plug");
      });
    }

    const connBadge = document.getElementById("museConnStatusBadge");
    if (connBadge) {
      connBadge.addEventListener("click", function () {
        if ((state.summary.total_nodes || 0) === 0) {
          switchRightSidebarTab("plug");
        } else {
          switchRightSidebarTab("nodes");
        }
      });
    }

    // 3. 4 Tab viên thuốc bên cột phải (Hôm nay / Tài khoản / Phiên gốc / Cắm nhanh)
    document.querySelectorAll(".four-tab-btn[data-side-tab]").forEach(function (tabBtn) {
      tabBtn.addEventListener("click", function () {
        switchRightSidebarTab(tabBtn.getAttribute("data-side-tab"));
      });
    });

    // 4. Nút nổi ☰ Đoạn chat (góc trên trái) và popover
    const btnToggleDrawer = document.getElementById("btnToggleThreadsDrawer");
    const threadsPopover = document.getElementById("threadsPopover");
    if (btnToggleDrawer && threadsPopover) {
      btnToggleDrawer.addEventListener("click", function (ev) {
        ev.stopPropagation();
        threadsPopover.classList.toggle("hidden-el");
      });
      document.addEventListener("click", function (ev) {
        if (
          !threadsPopover.classList.contains("hidden-el") &&
          !threadsPopover.contains(ev.target) &&
          ev.target !== btnToggleDrawer
        ) {
          threadsPopover.classList.add("hidden-el");
        }
      });
    }

    const nodePinSelect = document.getElementById("nodePinSelect");
    if (nodePinSelect) {
      nodePinSelect.addEventListener("change", function () {
        updateMuseSidebarStatus();
      });
    }

    // 5. Nút nổi 📝 Mới (góc trên phải)
    const btnNewThread = document.getElementById("btnNewThread");
    if (btnNewThread) {
      btnNewThread.addEventListener("click", async function () {
        try {
          const created = await apiRequest("/api/threads", {
            method: "POST",
            body: { title: "Đoạn chat mới", model: "" },
          });
          state.threads.unshift(created);
          state.activeThreadId = created.id;
          renderTodayActivityLists();
          renderActiveThreadMessages();
        } catch (err) {
          showToast("Lỗi tạo đoạn chat: " + err.message);
        }
      });
    }

    // 6. Các nút mở trình duyệt đăng nhập & quay lại
    const btnBackChat = document.getElementById("btnBackToChatView");
    if (btnBackChat) {
      btnBackChat.addEventListener("click", function () {
        switchMainView("chat");
      });
    }

    const btnGoFullAccounts = document.getElementById("btnGoFullAccounts");
    if (btnGoFullAccounts) {
      btnGoFullAccounts.addEventListener("click", function () {
        switchMainView("accounts");
      });
    }

    [
      "btnSideBrowserLogin",
      "btnBrowserInteractiveLogin",
      "btnOpenOAuthLinkWindow",
      "btnSideOpenOAuthWindow",
    ].forEach(function (id) {
      const el = document.getElementById(id);
      if (el) {
        el.addEventListener("click", function () {
          triggerInteractiveBrowserLogin(el.tagName === "BUTTON" ? el : null);
        });
      }
    });

    const btnExtAutoLoginNew = document.getElementById("btnExtAutoLoginNew");
    if (btnExtAutoLoginNew) {
      btnExtAutoLoginNew.addEventListener("click", async function () {
        await openRemoteBrowserLoginModal(btnExtAutoLoginNew);
      });
    }

    refreshCloudExtensionBanner();
    window.addEventListener("focus", function () {
      refreshCloudExtensionBanner();
    });

    ["btnGetOAuthLink", "btnFormGetOAuthLink", "btnSideGetOAuthLink"].forEach(function (id) {
      const el = document.getElementById(id);
      if (el) {
        el.addEventListener("click", function () {
          fetchAndShowOAuthLoginLink(el);
        });
      }
    });

    ["btnCopyOAuthLink", "btnSideCopyOAuthLink"].forEach(function (id) {
      const el = document.getElementById(id);
      if (el) {
        el.addEventListener("click", function () {
          if (state.lastOAuthLoginUrl) {
            navigator.clipboard.writeText(state.lastOAuthLoginUrl).then(function () {
              showToast("Đã sao chép Link Đăng Nhập OAuth vào bộ nhớ tạm!");
            });
          }
        });
      }
    });

    const btnOpenPinnedNative = document.getElementById("btnOpenPinnedNative");
    if (btnOpenPinnedNative) {
      btnOpenPinnedNative.addEventListener("click", async function () {
        const nodes = state.summary.nodes || [];
        if (nodes.length === 0) {
          showToast("Chưa có tài khoản nào. Đang mở cửa sổ đăng nhập Muse.ai...");
          await triggerInteractiveBrowserLogin(btnOpenPinnedNative);
          return;
        }
        const sel = document.getElementById("nodePinSelect");
        let targetId = sel && sel.value !== "auto" ? sel.value : "";
        if (!targetId) {
          const readyNode = nodes.find(function (n) {
            return n.status === "ready" || n.status === "running";
          });
          targetId = readyNode ? readyNode.id : nodes[0].id;
        }
        btnOpenPinnedNative.disabled = true;
        try {
          const res = await apiRequest("/api/accounts/open-native", {
            method: "POST",
            body: { node_id: targetId },
          });
          showToast(res.detail || "Đã mở cửa sổ Muse.ai gốc.");
        } catch (err) {
          showToast("Lỗi: " + err.message);
        } finally {
          btnOpenPinnedNative.disabled = false;
        }
      });
    }

    ["btnSyncAccountsPool", "btnWakeAllNodes", "btnSideWakeAll"].forEach(function (id) {
      const btn = document.getElementById(id);
      if (btn) {
        btn.addEventListener("click", async function () {
          const origText = btn.textContent;
          btn.disabled = true;
          btn.textContent = "⏳ Đang đồng bộ...";
          try {
            const res = await apiRequest("/api/accounts/wake", {
              method: "POST",
              body: { node_id: "all" },
            });
            showToast(
              "✅ Đã đồng bộ " +
                (res.probed || 0) +
                " tài khoản (" +
                (res.ready || 0) +
                " sẵn sàng gộp Token chung trong LibSQL)."
            );
            await refreshRouterState(true);
            await refreshLibSQLStatus();
          } catch (err) {
            showToast("Lỗi đồng bộ tài khoản: " + err.message);
          } finally {
            btn.disabled = false;
            btn.textContent = origText;
            populateNodePinSelector();
          }
        });
      }
    });

    // 7. Form thêm tài khoản (cả ở cột phải & trang quản lý tài khoản)
    const quickForm = document.getElementById("quickPlugForm");
    if (quickForm) {
      quickForm.addEventListener("submit", async function (ev) {
        ev.preventDefault();
        const label = (document.getElementById("quickLabelInput").value || "").trim();
        const proxyEl = document.getElementById("quickProxyInput");
        const proxyUrl = proxyEl ? proxyEl.value.trim() : "";
        const cred = (document.getElementById("quickCookieInput").value || "").trim();
        const btn = document.getElementById("btnQuickPlugSubmit");
        await submitCookieCredential(label, "cookie", "https://muse.ai", cred, btn, proxyUrl);
        document.getElementById("quickCookieInput").value = "";
      });
    }

    const addForm = document.getElementById("addAccountForm");
    if (addForm) {
      addForm.addEventListener("submit", async function (ev) {
        ev.preventDefault();
        const label = (document.getElementById("accLabelInput").value || "").trim();
        const authType = document.getElementById("accAuthTypeSelect").value;
        const upstreamUrl = (document.getElementById("accUpstreamInput").value || "").trim();
        const proxyEl = document.getElementById("accProxyInput");
        const proxyUrl = proxyEl ? proxyEl.value.trim() : "";
        const cred = (document.getElementById("accCredentialInput").value || "").trim();
        const btn = document.getElementById("btnSubmitAddAccount");
        await submitCookieCredential(label, authType, upstreamUrl, cred, btn, proxyUrl);
        document.getElementById("accCredentialInput").value = "";
      });
    }

    // 8. Đính kèm tệp / Dán ảnh chụp màn hình (Ctrl+V) & Gửi tin nhắn
    const btnAttach = document.getElementById("btnAttachFile");
    const fileInput = document.getElementById("chatFileInput");
    if (btnAttach && fileInput) {
      btnAttach.addEventListener("click", function () {
        fileInput.click();
      });
      fileInput.addEventListener("change", function () {
        if (fileInput.files && fileInput.files.length > 0) {
          addFilesToAttachments(fileInput.files);
          fileInput.value = "";
        }
      });
    }

    const composerForm = document.getElementById("chatComposerForm");
    const promptInput = document.getElementById("chatPromptInput");

    if (promptInput && composerForm) {
      promptInput.addEventListener("paste", function (ev) {
        const items = (ev.clipboardData || {}).items || [];
        const files = [];
        for (let i = 0; i < items.length; i++) {
          if (items[i].kind === "file") {
            const f = items[i].getAsFile();
            if (f) files.push(f);
          }
        }
        if (files.length > 0) {
          addFilesToAttachments(files);
        }
      });

      promptInput.addEventListener("keydown", function (ev) {
        if (ev.key === "Enter" && !ev.shiftKey) {
          ev.preventDefault();
          composerForm.requestSubmit();
        }
      });

      // --- Toggle icon mic ↔ send arrow khi người dùng nhập/xóa ---
      var iconMic = document.getElementById("iconMicDefault");
      var iconSend = document.getElementById("iconSendArrow");
      function updateSendIcon() {
        var hasText = promptInput.value.trim().length > 0 || state.pendingAttachments.length > 0;
        if (iconMic) iconMic.style.display = hasText ? "none" : "";
        if (iconSend) iconSend.style.display = hasText ? "" : "none";
      }
      promptInput.addEventListener("input", updateSendIcon);
      updateSendIcon();

      // --- Ô nhập tự căng cao + căng ngang theo nội dung (giống web gốc) ---
      var composerArea = promptInput.closest(".muse-bottom-composer-area");
      function autoGrowComposer() {
        promptInput.style.height = "auto";
        var maxH = 200;
        var h = Math.min(promptInput.scrollHeight, maxH);
        promptInput.style.height = h + "px";
        promptInput.style.overflowY = promptInput.scrollHeight > maxH ? "auto" : "hidden";
        if (composerArea) {
          var isMultiline = promptInput.scrollHeight > 30 || promptInput.value.indexOf("\n") !== -1;
          var isLong = promptInput.value.length > 40;
          composerArea.classList.toggle("composer-multiline", isMultiline);
          composerArea.classList.toggle("composer-expanded", isMultiline || isLong);
        }
      }
      promptInput.addEventListener("input", autoGrowComposer);
      autoGrowComposer();

      // --- Voice input (SpeechRecognition vi-VN) khi ô nhập trống ---
      var SpeechAPI = window.SpeechRecognition || window.webkitSpeechRecognition;
      var recognition = null;
      var isRecording = false;
      var micDot = document.getElementById("micRecordingDot");
      var sendBtn2 = document.getElementById("btnSendPrompt");

      if (SpeechAPI) {
        recognition = new SpeechAPI();
        recognition.lang = "vi-VN";
        recognition.continuous = false;
        recognition.interimResults = true;

        recognition.onstart = function() {
          isRecording = true;
          if (micDot) micDot.style.display = "";
          if (iconMic) iconMic.style.stroke = "#ef4444";
          showToast("🎤 Đang nghe... Hãy nói tiếng Việt");
        };
        recognition.onresult = function(event) {
          var transcript = "";
          for (var i = event.resultIndex; i < event.results.length; i++) {
            transcript += event.results[i][0].transcript;
          }
          promptInput.value = transcript;
          promptInput.dispatchEvent(new Event("input"));
          updateSendIcon();
        };
        recognition.onend = function() {
          isRecording = false;
          if (micDot) micDot.style.display = "none";
          if (iconMic) iconMic.style.stroke = "";
          // Nếu có text sau khi nói xong → tự động focus để người dùng Enter gửi
          if (promptInput.value.trim()) promptInput.focus();
        };
        recognition.onerror = function(e) {
          isRecording = false;
          if (micDot) micDot.style.display = "none";
          if (iconMic) iconMic.style.stroke = "";
          if (e.error !== "no-speech" && e.error !== "aborted") {
            showToast("❌ Lỗi giọng nói: " + e.error);
          }
        };
      }

      // Bấm nút khi ô trống → ghi âm; khi đang ghi → dừng
      if (sendBtn2) {
        sendBtn2.addEventListener("mousedown", function(ev) {
          // Chặn submit nếu ô nhập trống và SpeechRecognition có
          if (promptInput.value.trim().length === 0 && state.pendingAttachments.length === 0) {
            ev.preventDefault();
            if (!SpeechAPI) {
              showToast("⚠️ Trình duyệt không hỗ trợ nhận diện giọng nói.");
              return;
            }
            if (isRecording) {
              recognition.stop();
            } else {
              try { recognition.start(); } catch(e) { /* ignore */ }
            }
          }
        });
      }
    }

    if (composerForm) {
      composerForm.addEventListener("submit", async function (ev) {
        ev.preventDefault();
        const promptText = (promptInput.value || "").trim();
        if (!promptText && state.pendingAttachments.length === 0) return;

        const sendBtn = document.getElementById("btnSendPrompt");
        const selNode = document.getElementById("nodePinSelect");
        const attachmentsToSend = state.pendingAttachments.slice();

        if (!state.activeThreadId) {
          state.activeThreadId = "thread_" + Date.now();
        }
        const requestThreadId = state.activeThreadId;
        const localTurnId = "local_turn_" + Date.now() + "_" + Math.random().toString(36).slice(2, 7);

        promptInput.value = "";
        promptInput.dispatchEvent(new Event("input"));
        state.pendingAttachments = [];
        renderAttachmentPreviews();
        if (sendBtn) sendBtn.disabled = false;
        promptInput.focus();

        state.pendingChatCount = (state.pendingChatCount || 0) + 1;

        // Ẩn màn hình chào và hiện vùng tin nhắn ngay lập tức khi gửi tin nhắn
        const welcome = document.getElementById("chatWelcomeScreen");
        if (welcome) welcome.style.display = "none";

        // Hiển thị tức thì (0ms Optimistic UI) câu hỏi + dòng để trống giữ chỗ câu trả lời ngay bên dưới câu hỏi đó
        const viewport = document.getElementById("chatMessagesViewport");
        let uRow = null;
        let aiRow = null;
        if (viewport) {
          viewport.classList.remove("hidden-el");
          let streamInner = viewport.querySelector(".chat-stream-inner");
          if (!streamInner) {
            viewport.innerHTML = "";
            streamInner = document.createElement("div");
            streamInner.className = "chat-stream-inner";
            viewport.appendChild(streamInner);
          }

          uRow = document.createElement("div");
          uRow.className = "user-bubble-row";
          uRow.setAttribute("data-local-turn", localTurnId);
          const uCard = document.createElement("div");
          uCard.className = "user-bubble-card";
          if (attachmentsToSend.length > 0) {
            const attGrid = document.createElement("div");
            attGrid.className = "msg-attachments-grid";
            attachmentsToSend.forEach(function (att) {
              if (att.mime_type && att.mime_type.indexOf("image/") === 0 && att.data_url) {
                const img = document.createElement("img");
                img.src = att.data_url;
                img.alt = att.name || "Ảnh đính kèm";
                attGrid.appendChild(img);
              } else {
                const pill = document.createElement("span");
                pill.className = "file-tag-pill";
                pill.textContent = "📎 " + (att.name || "Tệp đính kèm");
                attGrid.appendChild(pill);
              }
            });
            uCard.appendChild(attGrid);
          }
          if (promptText) {
            const textDiv = document.createElement("div");
            textDiv.innerHTML = escapeHtml(promptText).replace(/\n/g, "<br>");
            uCard.appendChild(textDiv);
          }

          const uWrap = document.createElement("div");
          uWrap.className = "user-bubble-wrap";
          uWrap.appendChild(uCard);
          const uThumb = document.createElement("span");
          uThumb.className = "user-bubble-reaction";
          uThumb.innerHTML =
            '<img src="/avatars/facebook_thumbs_up_v1.png" width="13" height="13" style="width:13px;height:13px;object-fit:contain;display:block;" alt="👍">';
          uWrap.appendChild(uThumb);

          uRow.appendChild(uWrap);
          streamInner.appendChild(uRow);

          // Dòng trả lời để trống ngay bên dưới câu hỏi vừa gửi, chờ điền câu trả lời vào đúng vị trí này
          aiRow = document.createElement("div");
          aiRow.className = "ai-bubble-row pending-answer-row";
          aiRow.setAttribute("data-local-turn", localTurnId);
          const aiCard = document.createElement("div");
          aiCard.className = "ai-bubble-card typing-dots-bubble";
          aiCard.innerHTML =
            '<span class="typing-dot"></span>' +
            '<span class="typing-dot"></span>' +
            '<span class="typing-dot"></span>';
          aiRow.appendChild(aiCard);
          streamInner.appendChild(aiRow);

          viewport.scrollTop = viewport.scrollHeight;
        }

        // Bật trạng thái Con Mèo Đẩy Xe Hàng Mart AI + nút tròn đen ■ dừng phản hồi
        state.bgActive = false;
        var currentCartPct = 14;
        setMuseCharacterActivity(true, promptText, "", false, currentCartPct);

        var livePollTimer = setInterval(async function () {
          if (!state.isGenerating) return;
          currentCartPct = Math.min(88, currentCartPct + 6);
          try {
            const live = await apiRequest("/api/chat/live-status");
            if (live && live.active) {
              const mdLen = String(live.partial_md || "").length;
              if (mdLen > 0) {
                const streamPct = Math.min(94, 26 + Math.floor(mdLen / 15));
                if (streamPct > currentCartPct) {
                  currentCartPct = streamPct;
                }
              }
              setMuseCharacterActivity(
                true,
                live.status_text || promptText,
                "",
                Boolean(live.status_text),
                currentCartPct
              );
              return;
            }
          } catch (_) {
            // Bỏ qua lỗi mạng tạm thời khi đang poll trạng thái
          }
          setMuseCharacterActivity(true, promptText, "", false, currentCartPct);
        }, 420);

        const stopBtn = document.getElementById("btnStopGenerating");
        var stoppedByUser = false;
        if (stopBtn) {
          stopBtn.onclick = function () {
            stoppedByUser = true;
            clearInterval(livePollTimer);
            state.pendingChatCount = Math.max(0, (state.pendingChatCount || 1) - 1);
            if (aiRow && aiRow.parentNode) aiRow.remove();
            if (state.pendingChatCount === 0) {
              setMuseCharacterActivity(false);
            }
            if (sendBtn) sendBtn.disabled = false;
            showToast("Đã dừng phản hồi.");
          };
        }

        try {
          // Inject active memories vào prompt (nếu có bộ nhớ đang bật)
          var finalPrompt = promptText || "Hãy phân tích tệp đính kèm";
          try {
            var memResult = await libsqlExec(
              "SELECT content FROM muse_memories WHERE is_active=1 ORDER BY created_at ASC LIMIT 10"
            );
            var memRows = libsqlRows(memResult);
            if (memRows.length > 0) {
              var memBlock = memRows.map(function(r){ return r.content; }).join("\n");
              finalPrompt = "[Hướng dẫn tùy chỉnh]\n" + memBlock + "\n\n[Yêu cầu]\n" + finalPrompt;
            }
          } catch(_) { /* không có memories — bỏ qua */ }

          let res = await apiRequest("/api/chat/send", {
            method: "POST",
            body: {
              thread_id: requestThreadId,
              preferred_node_id: selNode ? selNode.value : "auto",
              model: "",
              workspace: "chat",
              prompt: finalPrompt,
              attachments: attachmentsToSend,
            },
          });

          if (stoppedByUser) {
            if (res && res.browser_relay) {
              await apiRequest("/api/chat/relay-complete", {
                method: "POST",
                body: {
                  thread_id: res.thread_id || "",
                  turn_id: res.turn_id || "",
                  node_id: res.node_id || "",
                  model: res.model || "",
                  error: "USER_STOPPED",
                },
              }).catch(function () {});
            }
            return;
          }

          if (res && res.browser_relay) {
            res = await executeBrowserRelayIfNeeded(res, updateMartCartProgress);
            if (stoppedByUser) return;
          }

          // Điền câu trả lời NGAY VÀO DÒNG ĐỂ TRỐNG ngay bên dưới câu hỏi đó
          if (aiRow && aiRow.parentNode) {
            if (res.turn_id) {
              aiRow.setAttribute("data-turn-id", res.turn_id);
              if (uRow) uRow.setAttribute("data-turn-id", res.turn_id);
            }
            fillAiBubbleRow(
              aiRow,
              res.reply || res.content || "",
              res.node && res.node.label ? res.node.label : "",
              promptText
            );
          }

          clearInterval(livePollTimer);
          updateMartCartProgress(100, "Xe hàng đã đầy đồ!");

          if (res.thread) {
            state.activeThreadId = res.thread.id;
            const idx = state.threads.findIndex(function (t) {
              return t.id === res.thread.id;
            });
            if (idx >= 0) {
              state.threads[idx] = mergeThreadPreservingCompletedTurns(state.threads[idx], res.thread);
            } else {
              state.threads.unshift(res.thread);
            }
          }
          if (res.summary) {
            state.summary = res.summary;
            const sig = JSON.stringify(res.summary.nodes || []);
            if (sig !== state.lastNodesSignature) {
              state.lastNodesSignature = sig;
              populateNodePinSelector();
              renderSideNodesList();
              renderAccountsTable();
            }
          }

          renderTodayActivityLists();
          refreshLibSQLStatus();

          // Auto-extract: tự tìm code blocks & markdown trong reply → lưu vào muse_documents
          try {
            var replyContent = res.reply || res.content || "";
            autoExtractDocuments(replyContent, state.activeThreadId);
          } catch(_) { /* bỏ qua lỗi extract */ }

          // Thread auto-rename: nếu thread vừa tạo (title = "Đoạn chat mới"), đổi tên từ prompt đầu tiên
          try {
            var autoTitleThreadId = state.activeThreadId;
            var curThread = state.threads.find(function(t){ return t.id === autoTitleThreadId; });
            if (curThread && (curThread.title === "Đoạn chat mới" || curThread.title === "New Chat" || !curThread.title)) {
              // Strip prefix memory nếu có
              var rawPrompt = promptText || "";
              var memPrefixIdx = rawPrompt.indexOf("[Yêu cầu]\n");
              var cleanPrompt = memPrefixIdx >= 0 ? rawPrompt.slice(memPrefixIdx + 10) : rawPrompt;
              var autoTitle = cleanPrompt.replace(/\n/g, " ").trim().slice(0, 60) || "Đoạn chat";
              if (autoTitle.length === 60) autoTitle += "…";
              await libsqlExec(
                "UPDATE chat_threads SET title=?, updated_at=? WHERE id=?",
                [autoTitle, nowISO(), autoTitleThreadId]
              );
              curThread.title = autoTitle;
              // Cập nhật sidebar
              var threadItem = document.querySelector(".thread-item[data-id='" + autoTitleThreadId + "'] .thread-title");
              if (threadItem) threadItem.textContent = autoTitle;
            }
          } catch(_) { /* bỏ qua lỗi auto-rename */ }

          // Browser Notification: thông báo khi Mart AI reply và tab không được focus
          try {
            var notifReply = res.reply || res.content || "";
            if (notifReply && !document.hasFocus() && "Notification" in window) {
              if (Notification.permission === "granted") {
                var snippet = notifReply.replace(/[#*`\n]/g, " ").trim().slice(0, 80);
                new Notification("Mart AI đã trả lời", {
                  body: snippet || "Nhấn để xem phản hồi.",
                  icon: "/favicon.ico"
                });
              } else if (Notification.permission === "default") {
                Notification.requestPermission();
              }
            }
          } catch(_) { /* bỏ qua lỗi notification */ }
        } catch (err) {
          if (!stoppedByUser) {
            if (aiRow && aiRow.parentNode) aiRow.remove();
            if (uRow && uRow.parentNode) uRow.remove();
            if (!promptInput.value) {
              promptInput.value = promptText;
              promptInput.dispatchEvent(new Event("input"));
            }
            showToast(err.message);
          }
        } finally {
          clearInterval(livePollTimer);
          if (!stoppedByUser) {
            state.pendingChatCount = Math.max(0, (state.pendingChatCount || 1) - 1);
          }
          if (state.pendingChatCount === 0) {
            setMuseCharacterActivity(false);
            renderActiveThreadMessages();
          }
          if (sendBtn) sendBtn.disabled = false;
        }
      });
    }

    // 9. Điều khiển & Truy vấn trực tiếp Cơ sở dữ liệu chuẩn LibSQL (/v2/pipeline)
    document.querySelectorAll(".btn-libsql-preset").forEach(function (btn) {
      btn.addEventListener("click", function () {
        const sql = btn.getAttribute("data-sql") || "";
        const sqlInput = document.getElementById("libsqlSqlInput");
        if (sqlInput && sql) {
          sqlInput.value = sql;
          executeAndRenderLibSQLQuery(sql);
        }
      });
    });

    const libsqlQueryForm = document.getElementById("libsqlQueryForm");
    if (libsqlQueryForm) {
      libsqlQueryForm.addEventListener("submit", async function (ev) {
        ev.preventDefault();
        const sqlInput = document.getElementById("libsqlSqlInput");
        if (sqlInput && sqlInput.value.trim()) {
          await executeAndRenderLibSQLQuery(sqlInput.value.trim());
        }
      });
    }

    const libsqlConfigForm = document.getElementById("libsqlConfigForm");
    if (libsqlConfigForm) {
      libsqlConfigForm.addEventListener("submit", async function (ev) {
        ev.preventDefault();
        const urlEl = document.getElementById("libsqlUrlInput");
        const tokEl = document.getElementById("libsqlTokenInput");
        const btnEl = document.getElementById("btnApplyLibsqlConfig");
        if (btnEl) btnEl.disabled = true;
        try {
          await apiRequest("/api/libsql/config", {
            method: "POST",
            body: {
              database_url: urlEl ? urlEl.value.trim() : "",
              auth_token: tokEl ? tokEl.value.trim() : "",
            },
          });
          showToast("✅ Đã kết nối & đồng bộ Cơ sở dữ liệu LibSQL thành công!");
          await refreshLibSQLStatus();
          await refreshRouterState(true);
          await loadThreads();
        } catch (err) {
          showToast("Lỗi cấu hình LibSQL: " + err.message);
        } finally {
          if (btnEl) btnEl.disabled = false;
        }
      });
    }
  }

  var bgCartPct = 0;
  var bgPollBusy = false;
  var silentSyncBusy = false;

  async function silentAutoSyncAccounts() {
    if (silentSyncBusy) return;
    const nodes = (state.summary && state.summary.nodes) || [];
    if (nodes.length === 0) return;
    silentSyncBusy = true;
    try {
      await apiRequest("/api/accounts/wake", {
        method: "POST",
        body: { node_id: "all" },
      });
      await refreshRouterState(true);
    } catch (_) {
      // Bỏ qua lỗi mạng tạm thời khi đồng bộ ngầm
    } finally {
      silentSyncBusy = false;
    }
  }

  async function pollBackgroundActivity() {
    if (bgPollBusy) return;
    bgPollBusy = true;
    try {
      const live = await apiRequest("/api/chat/live-status");

      // Nếu Muse gốc gửi thêm kết quả sau khi hoàn tất việc nền (ví dụ: viết code / đọc bộ nhớ xong),
      // tự động đồng bộ ngay vào đúng câu hỏi đó trên giao diện
      if (live && Array.isArray(live.updated_threads) && live.updated_threads.length > 0) {
        live.updated_threads.forEach(function (ut) {
          if (!ut || !ut.id) return;
          const idx = state.threads.findIndex(function (t) {
            return t.id === ut.id;
          });
          if (idx >= 0) {
            state.threads[idx] = mergeThreadPreservingCompletedTurns(state.threads[idx], ut);
          } else {
            state.threads.unshift(ut);
          }
          if (ut.id === state.activeThreadId) {
            if ((state.pendingChatCount || 0) === 0) {
              renderActiveThreadMessages();
            } else if (Array.isArray(ut.messages)) {
              ut.messages.forEach(function (m) {
                if (m.role !== "user" && m.turn_id && !m.pending && String(m.content || "").trim()) {
                  const rowEl = document.querySelector('.ai-bubble-row[data-turn-id="' + m.turn_id + '"]');
                  if (rowEl) {
                    fillAiBubbleRow(rowEl, m.content || "", m.node_label || "", "");
                  }
                }
              });
            }
          }
        });
        renderTodayActivityLists();
      }

      if (state.isGenerating && !state.bgActive) return;
      const stopBtn = document.getElementById("btnStopGenerating");
      if (live && live.active) {
        state.bgActive = true;
        bgCartPct = Math.min(90, (bgCartPct || 20) + 3);
        setMuseCharacterActivity(true, live.status_text || "", "", Boolean(live.status_text), bgCartPct);
        if (stopBtn) stopBtn.classList.add("hidden-el");
      } else if (state.bgActive) {
        state.bgActive = false;
        bgCartPct = 0;
        updateMartCartProgress(100, "Hoàn tất");
        setTimeout(function () {
          if (!state.bgActive) setMuseCharacterActivity(false);
        }, 600);
        refreshRouterState(true);
      }
    } catch (_) {
      // Bỏ qua lỗi mạng tạm thời
    } finally {
      bgPollBusy = false;
    }
  }

  async function initSession() {
    try {
      const auth = await apiRequest("/api/auth/session");
      if (auth && auth.token) {
        state.jwtToken = auth.token;
      }
    } catch (err) {
      console.warn("Session init:", err);
    }

    setupEventListeners();
    await refreshRouterState(true);
    await loadThreads();
    await refreshLibSQLStatus();

    setInterval(function () {
      refreshRouterState();
    }, 5000);

    // Theo dõi trạng thái làm việc nền của Muse gốc (đọc bộ nhớ, viết code...) kể cả sau khi đã có câu trả lời
    setInterval(pollBackgroundActivity, 1500);

    // Wire Search panel
    wireSearchPanel();
    // Wire Docs panel
    wireDocsPanel();
    // Wire Memories panel
    wireMemoriesPanel();
    // Wire Tasks panel
    wireTasksPanel();
    // Wire Library panel
    wireLibraryPanel();

    // Kiểm tra URL khởi tạo: nếu truy cập trang cài đặt riêng (/cai-dat hoặc /admin)
    var initPath = window.location.pathname;
    if (initPath === "/cai-dat" || initPath === "/admin" || window.location.hash === "#cai-dat") {
      switchMainView("accounts");
    } else if (initPath === "/library/artifacts") {
      switchMainView("library");
    }

    // Keyboard shortcuts: Ctrl+K → Search, Ctrl+N → Chat mới, Ctrl+Shift+S → Trang cài đặt riêng, Esc → Chat
    document.addEventListener("keydown", function(ev) {
      // Phím tắt bí mật chỉ mình chủ máy biết: Ctrl + Shift + S -> Mở Trang Cài Đặt Riêng
      if (ev.ctrlKey && ev.shiftKey && (ev.key === "S" || ev.key === "s")) {
        ev.preventDefault();
        switchMainView(state.viewMode === "accounts" ? "chat" : "accounts");
        return;
      }
      // Không trigger khi đang gõ trong input/textarea
      var tag = (ev.target || {}).tagName || "";
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") {
        // Chỉ cho phép Esc thoát khỏi input
        if (ev.key === "Escape") { ev.target.blur(); }
        return;
      }
      if (ev.ctrlKey && ev.key === "k") {
        ev.preventDefault();
        switchMainView("search");
        var searchInput = document.getElementById("searchInput");
        if (searchInput) { searchInput.focus(); }
        showToast("🔍 Tìm kiếm (Ctrl+K)");
      } else if (ev.ctrlKey && ev.key === "n") {
        ev.preventDefault();
        switchMainView("chat");
        var btnNew = document.getElementById("btnNewThread");
        if (btnNew) btnNew.click();
        showToast("💬 Đoạn chat mới (Ctrl+N)");
      } else if (ev.key === "Escape") {
        ev.preventDefault();
        switchMainView("chat");
      }
    });

    // Xin quyền Browser Notification khi khởi động (nếu chưa có)
    if ("Notification" in window && Notification.permission === "default") {
      Notification.requestPermission();
    }
  }

  /* ====================================================================
     HELPER: Chạy pipeline libSQL qua Hrana v2 (POST /v2/pipeline)
     ==================================================================== */
  async function libsqlExec(sql, args) {
    args = args || [];
    var hranaArgs = args.map(function(v) {
      if (v === null || v === undefined) return { type: "null" };
      if (typeof v === "number" && Number.isInteger(v)) return { type: "integer", value: String(v) };
      if (typeof v === "number") return { type: "float", value: v };
      return { type: "text", value: String(v) };
    });
    var body = {
      requests: [
        { type: "execute", stmt: { sql: sql, args: hranaArgs, want_rows: true } },
        { type: "close" }
      ]
    };
    var resp = await fetch("/v2/pipeline", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    if (!resp.ok) throw new Error("libSQL pipeline HTTP " + resp.status);
    var data = await resp.json();
    var r0 = data.results && data.results[0];
    if (!r0 || r0.type !== "ok") throw new Error((r0 && r0.error && r0.error.message) || "libSQL error");
    return r0.response && r0.response.result;
  }

  function libsqlRows(result) {
    if (!result || !result.rows) return [];
    var cols = (result.cols || []).map(function(c){ return c.name; });
    return result.rows.map(function(row) {
      var obj = {};
      cols.forEach(function(col, i) { obj[col] = row[i] ? row[i].value : null; });
      return obj;
    });
  }

  function nowISO() { return new Date().toISOString().replace("T"," ").slice(0,19); }
  function uuid4() {
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, function(c){
      var r = Math.random()*16|0, v = c==="x"?r:(r&0x3|0x8);
      return v.toString(16);
    });
  }

  /* ====================================================================
     🔍 PANEL SEARCH — Tìm kiếm toàn văn trong libSQL
     ==================================================================== */
  function wireSearchPanel() {
    var btnSearch = document.getElementById("btnRunSearch");
    var searchInput = document.getElementById("searchInput");
    if (!btnSearch || !searchInput) return;

    async function doSearch() {
      var q = searchInput.value.trim();
      if (!q) return;
      var countEl = document.getElementById("searchResultsCount");
      var listEl = document.getElementById("searchResultsList");
      listEl.innerHTML = "<p style='color:#737373;font-size:13px;'>Đang tìm kiếm...</p>";
      try {
        var like = "%" + q + "%";
        // Tìm trong threads
        var trResult = await libsqlExec(
          "SELECT id, title, updated_at FROM chat_threads WHERE title LIKE ? ORDER BY updated_at DESC LIMIT 20", [like]
        );
        var threads = libsqlRows(trResult);
        // Tìm trong messages
        var msgResult = await libsqlExec(
          "SELECT m.id, m.thread_id, m.role, substr(m.content,1,200) AS snippet, m.timestamp, t.title AS thread_title FROM chat_messages m LEFT JOIN chat_threads t ON t.id=m.thread_id WHERE m.content LIKE ? ORDER BY m.timestamp DESC LIMIT 30", [like]
        );
        var msgs = libsqlRows(msgResult);
        var total = threads.length + msgs.length;
        if (countEl) countEl.textContent = 'Tìm thấy ' + total + ' kết quả cho "' + q + '"';
        listEl.innerHTML = "";
        if (total === 0) {
          listEl.innerHTML = "<p style='text-align:center;color:#737373;padding:30px 0;'>Không tìm thấy kết quả nào.</p>";
          return;
        }
        threads.forEach(function(t) {
          var card = document.createElement("div");
          card.className = "result-card";
          card.innerHTML = '<div class="result-card-title">💬 ' + escapeHtml(t.title || "Đoạn chat không có tiêu đề") + '</div>' +
            '<div class="result-card-meta">Đoạn chat · ' + escapeHtml(t.updated_at || "") + '</div>';
          card.addEventListener("click", function() {
            switchMainView("chat");
            loadThread(t.id);
          });
          listEl.appendChild(card);
        });
        msgs.forEach(function(m) {
          var card = document.createElement("div");
          card.className = "result-card";
          var roleIcon = m.role === "user" ? "👤" : "🤖";
          card.innerHTML = '<div class="result-card-title">' + roleIcon + ' ' + escapeHtml(m.thread_title || "Đoạn chat") + '</div>' +
            '<div class="result-card-snippet">' + escapeHtml((m.snippet||"").trim()) + '</div>' +
            '<div class="result-card-meta">' + escapeHtml(m.role||"") + ' · ' + escapeHtml(m.timestamp||"") + '</div>';
          card.addEventListener("click", function() {
            switchMainView("chat");
            loadThread(m.thread_id);
          });
          listEl.appendChild(card);
        });
      } catch(e) {
        if (listEl) listEl.innerHTML = "<p style='color:#dc2626;'>Lỗi tìm kiếm: " + escapeHtml(e.message) + "</p>";
      }
    }

    btnSearch.addEventListener("click", doSearch);
    searchInput.addEventListener("keydown", function(e) {
      if (e.key === "Enter") doSearch();
    });
  }

  /* ====================================================================
     📄 PANEL DOCS — Tài liệu & Artifacts từ libSQL (muse_documents)
     ==================================================================== */
  function wireDocsPanel() {
    var btnNew = document.getElementById("btnNewDoc");
    var btnSave = document.getElementById("btnSaveDoc");
    var btnCancel = document.getElementById("btnCancelDoc");
    if (!btnNew || !btnSave) return;

    btnNew.addEventListener("click", function() {
      document.getElementById("docEditingId").value = "";
      document.getElementById("docTitleInput").value = "";
      document.getElementById("docContentInput").value = "";
      document.getElementById("docEditorCard").classList.remove("hidden-el");
      document.getElementById("docTitleInput").focus();
    });
    btnCancel.addEventListener("click", function() {
      document.getElementById("docEditorCard").classList.add("hidden-el");
    });
    btnSave.addEventListener("click", async function() {
      var id = document.getElementById("docEditingId").value || uuid4();
      var title = document.getElementById("docTitleInput").value.trim();
      var content = document.getElementById("docContentInput").value.trim();
      if (!title) { showToast("⚠️ Vui lòng nhập tiêu đề tài liệu."); return; }
      if (!content) { showToast("⚠️ Vui lòng nhập nội dung tài liệu."); return; }
      try {
        await libsqlExec(
          "INSERT INTO muse_documents(id,title,content,updated_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title, content=excluded.content, updated_at=excluded.updated_at",
          [id, title, content, nowISO()]
        );
        document.getElementById("docEditorCard").classList.add("hidden-el");
        showToast("✅ Đã lưu tài liệu vào libSQL.");
        loadDocsList();
      } catch(e) { showToast("❌ Lỗi lưu tài liệu: " + e.message); }
    });
  }

  async function loadDocsList() {
    var listEl = document.getElementById("docsList");
    var emptyEl = document.getElementById("docsEmptyState");
    if (!listEl) return;
    // Loading spinner
    listEl.innerHTML = '<div style="text-align:center;padding:24px;color:#94a3b8;font-size:13px;">⏳ Đang tải tài liệu...</div>';
    if (emptyEl) emptyEl.style.display = "none";
    try {
      var result = await libsqlExec("SELECT id, title, substr(content,1,200) AS preview, updated_at FROM muse_documents ORDER BY updated_at DESC LIMIT 100");
      var rows = libsqlRows(result);
      if (rows.length === 0) {
        if (emptyEl) emptyEl.style.display = "";
        listEl.innerHTML = "";
        return;
      }
      if (emptyEl) emptyEl.style.display = "none";
      listEl.innerHTML = "";
      rows.forEach(function(doc) {
        var card = document.createElement("div");
        card.className = "result-card";
        card.innerHTML =
          '<div class="result-card-title">📄 ' + escapeHtml(doc.title||"Không có tiêu đề") + '</div>' +
          '<div class="result-card-snippet">' + escapeHtml((doc.preview||"").trim()) + '</div>' +
          '<div class="result-card-meta">Cập nhật: ' + escapeHtml(doc.updated_at||"") + '</div>' +
          '<div class="result-card-actions">' +
            '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;" data-action="edit-doc" data-id="' + escapeHtml(doc.id) + '">✏️ Sửa</button>' +
            '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;" data-action="copy-doc" data-id="' + escapeHtml(doc.id) + '">📋 Sao chép</button>' +
            '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;" data-action="export-doc" data-id="' + escapeHtml(doc.id) + '" data-title="' + escapeHtml(doc.title||"tai-lieu") + '">⬇️ .md</button>' +
            '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;color:#dc2626;border-color:#dc2626;" data-action="del-doc" data-id="' + escapeHtml(doc.id) + '">🗑️ Xóa</button>' +
          '</div>';
        listEl.appendChild(card);
      });
      // Gán sự kiện nút
      listEl.querySelectorAll("[data-action='edit-doc']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          var id = btn.getAttribute("data-id");
          try {
            var r = await libsqlExec("SELECT id,title,content FROM muse_documents WHERE id=?", [id]);
            var rows2 = libsqlRows(r);
            if (!rows2.length) return;
            document.getElementById("docEditingId").value = rows2[0].id;
            document.getElementById("docTitleInput").value = rows2[0].title||"";
            document.getElementById("docContentInput").value = rows2[0].content||"";
            document.getElementById("docEditorCard").classList.remove("hidden-el");
            document.getElementById("docTitleInput").focus();
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
      listEl.querySelectorAll("[data-action='copy-doc']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          var id = btn.getAttribute("data-id");
          try {
            var r = await libsqlExec("SELECT content FROM muse_documents WHERE id=?", [id]);
            var rows2 = libsqlRows(r);
            if (!rows2.length) return;
            await navigator.clipboard.writeText(rows2[0].content||"");
            showToast("✅ Đã sao chép nội dung tài liệu.");
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
      // Export .md
      listEl.querySelectorAll("[data-action='export-doc']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          var id = btn.getAttribute("data-id");
          var title = btn.getAttribute("data-title") || "tai-lieu";
          try {
            var r = await libsqlExec("SELECT title,content FROM muse_documents WHERE id=?", [id]);
            var rows2 = libsqlRows(r);
            if (!rows2.length) return;
            var mdContent = "# " + (rows2[0].title||"Tài liệu") + "\n\n" + (rows2[0].content||"");
            var blob = new Blob([mdContent], { type: "text/markdown;charset=utf-8" });
            var url = URL.createObjectURL(blob);
            var a = document.createElement("a");
            a.href = url;
            a.download = title.replace(/[^a-z0-9\u00C0-\u024F\s-]/gi, "").trim().replace(/\s+/g, "-") + ".md";
            document.body.appendChild(a);
            a.click();
            setTimeout(function(){ URL.revokeObjectURL(url); document.body.removeChild(a); }, 1000);
            showToast("⬇️ Đã tải xuống tài liệu .md");
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
      listEl.querySelectorAll("[data-action='del-doc']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          if (!confirm("Xóa tài liệu này?")) return;
          var id = btn.getAttribute("data-id");
          try {
            await libsqlExec("DELETE FROM muse_documents WHERE id=?", [id]);
            showToast("✅ Đã xóa tài liệu.");
            loadDocsList();
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
    } catch(e) { if (listEl) listEl.innerHTML = "<p style='color:#dc2626;'>Lỗi tải tài liệu: " + escapeHtml(e.message) + "</p>"; }
  }

  /* ====================================================================
     💡 PANEL IDEAS — Bộ nhớ & Prompt mẫu (muse_memories)
     ==================================================================== */
  function wireMemoriesPanel() {
    var btnNew = document.getElementById("btnNewMemory");
    var btnSave = document.getElementById("btnSaveMemory");
    var btnCancel = document.getElementById("btnCancelMemory");
    if (!btnNew || !btnSave) return;

    btnNew.addEventListener("click", function() {
      document.getElementById("memoryEditingId").value = "";
      document.getElementById("memoryTitleInput").value = "";
      document.getElementById("memoryContentInput").value = "";
      document.getElementById("memoryEditorCard").classList.remove("hidden-el");
      document.getElementById("memoryTitleInput").focus();
    });
    btnCancel.addEventListener("click", function() {
      document.getElementById("memoryEditorCard").classList.add("hidden-el");
    });
    btnSave.addEventListener("click", async function() {
      var id = document.getElementById("memoryEditingId").value || uuid4();
      var title = document.getElementById("memoryTitleInput").value.trim();
      var content = document.getElementById("memoryContentInput").value.trim();
      if (!title || !content) { showToast("⚠️ Vui lòng nhập đủ tiêu đề và nội dung."); return; }
      try {
        await libsqlExec(
          "INSERT INTO muse_memories(id,title,content,is_active,created_at) VALUES(?,?,?,1,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title, content=excluded.content",
          [id, title, content, nowISO()]
        );
        document.getElementById("memoryEditorCard").classList.add("hidden-el");
        showToast("✅ Đã lưu bộ nhớ vào libSQL.");
        loadMemoriesList();
      } catch(e) { showToast("❌ " + e.message); }
    });
  }

  async function loadMemoriesList() {
    var listEl = document.getElementById("memoriesList");
    var emptyEl = document.getElementById("memoriesEmptyState");
    if (!listEl) return;
    // Loading spinner
    listEl.innerHTML = '<div style="text-align:center;padding:24px;color:#94a3b8;font-size:13px;">⏳ Đang tải bộ nhớ...</div>';
    if (emptyEl) emptyEl.style.display = "none";
    try {
      var result = await libsqlExec("SELECT id, title, content, is_active, created_at FROM muse_memories ORDER BY created_at DESC LIMIT 100");
      var rows = libsqlRows(result);
      if (rows.length === 0) {
        if (emptyEl) emptyEl.style.display = "";
        listEl.innerHTML = "";
        return;
      }
      if (emptyEl) emptyEl.style.display = "none";
      listEl.innerHTML = "";
      rows.forEach(function(mem) {
        var isActive = mem.is_active === "1" || mem.is_active === 1;
        var card = document.createElement("div");
        card.className = "result-card";
        card.innerHTML =
          '<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:10px;">' +
            '<div style="flex:1;">' +
              '<div class="result-card-title">💡 ' + escapeHtml(mem.title||"Không có tiêu đề") + '</div>' +
              '<div class="result-card-snippet">' + escapeHtml((mem.content||"").slice(0,200)) + '</div>' +
              '<div class="result-card-meta">Tạo: ' + escapeHtml(mem.created_at||"") + '</div>' +
            '</div>' +
            '<label class="memory-toggle" title="' + (isActive?"Đang bật — Muse sẽ nhớ điều này":"Đang tắt") + '">' +
              '<input type="checkbox" class="mem-toggle-chk" data-id="' + escapeHtml(mem.id) + '"' + (isActive?" checked":"") + '>' +
              '<span class="memory-toggle-slider"></span>' +
            '</label>' +
          '</div>' +
          '<div class="result-card-actions">' +
            '<button type="button" class="muse-solid-btn" style="font-size:12px;min-height:28px;" data-action="run-memory" data-content="' + escapeHtml(mem.content||"") + '">▶️ Chạy ngay trong Chat</button>' +
            '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;color:#dc2626;border-color:#dc2626;" data-action="del-memory" data-id="' + escapeHtml(mem.id) + '">🗑️ Xóa</button>' +
          '</div>';
        listEl.appendChild(card);
      });
      // Toggle bật/tắt bộ nhớ
      listEl.querySelectorAll(".mem-toggle-chk").forEach(function(chk) {
        chk.addEventListener("change", async function() {
          var id = chk.getAttribute("data-id");
          var val = chk.checked ? 1 : 0;
          try {
            await libsqlExec("UPDATE muse_memories SET is_active=? WHERE id=?", [val, id]);
            showToast(chk.checked ? "✅ Bộ nhớ đã bật." : "ℹ️ Bộ nhớ đã tắt.");
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
      // Chạy ngay trong Chat
      listEl.querySelectorAll("[data-action='run-memory']").forEach(function(btn) {
        btn.addEventListener("click", function() {
          var content = btn.getAttribute("data-content");
          switchMainView("chat");
          var input = document.getElementById("chatPromptInput");
          if (input) { input.value = content; input.dispatchEvent(new Event("input")); input.focus(); }
          showToast("📋 Đã chèn vào ô nhập. Nhấn Enter để gửi.");
        });
      });
      // Xóa bộ nhớ
      listEl.querySelectorAll("[data-action='del-memory']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          if (!confirm("Xóa bộ nhớ này?")) return;
          var id = btn.getAttribute("data-id");
          try {
            await libsqlExec("DELETE FROM muse_memories WHERE id=?", [id]);
            showToast("✅ Đã xóa bộ nhớ.");
            loadMemoriesList();
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
    } catch(e) { if (listEl) listEl.innerHTML = "<p style='color:#dc2626;'>Lỗi tải bộ nhớ: " + escapeHtml(e.message) + "</p>"; }
  }

  /* ====================================================================
     ☑️ PANEL TASKS — Nhiệm vụ & Hàng đợi lệnh (muse_tasks)
     ==================================================================== */
  var currentTaskFilter = "all";

  function wireTasksPanel() {
    var btnNew = document.getElementById("btnNewTask");
    var btnSave = document.getElementById("btnSaveTask");
    var btnCancel = document.getElementById("btnCancelTask");
    if (!btnNew || !btnSave) return;

    // Nút "⚡ Chạy tất cả" — batch concurrent qua /api/tasks/batch
    var btnRunAll = document.getElementById("btnRunAllPending");
    if (btnRunAll) {
      btnRunAll.addEventListener("click", async function() {
        try {
          // Lấy tất cả task pending từ libSQL
          var result = await libsqlExec(
            "SELECT id, title, prompt FROM muse_tasks WHERE status='pending' ORDER BY updated_at ASC LIMIT 10"
          );
          var rows = libsqlRows(result);
          if (rows.length === 0) {
            showToast("⚠️ Không có nhiệm vụ nào đang chờ xử lý.");
            return;
          }
          btnRunAll.disabled = true;
          btnRunAll.textContent = "⏳ Đang chạy " + rows.length + " nhiệm vụ...";
          showToast("⚡ Đang chạy " + rows.length + " nhiệm vụ song song...");

          var tasks = rows.map(function(r) {
            return { task_id: r.id, prompt: r.prompt, title: r.title || "" };
          });
          var resp = await fetch("/api/tasks/batch", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ tasks: tasks })
          });
          if (!resp.ok) {
            var errData = await resp.json().catch(function(){ return {}; });
            throw new Error(errData.error || "HTTP " + resp.status);
          }
          var data = await resp.json();
          var doneCount = (data.results || []).filter(function(r){ return r.status === "done"; }).length;
          var errCount  = (data.results || []).filter(function(r){ return r.status === "error"; }).length;
          showToast("✅ Hoàn thành " + doneCount + "/" + rows.length + " nhiệm vụ" + (errCount > 0 ? " · " + errCount + " lỗi" : "") + ".");
          loadTasksList(currentTaskFilter);
        } catch(e) {
          showToast("❌ Lỗi batch: " + e.message);
        } finally {
          btnRunAll.disabled = false;
          btnRunAll.textContent = "⚡ Chạy tất cả";
        }
      });
    }

    btnNew.addEventListener("click", function() {
      document.getElementById("taskEditingId").value = "";
      document.getElementById("taskTitleInput").value = "";
      document.getElementById("taskPromptInput").value = "";
      document.getElementById("taskEditorCard").classList.remove("hidden-el");
      document.getElementById("taskTitleInput").focus();
    });
    btnCancel.addEventListener("click", function() {
      document.getElementById("taskEditorCard").classList.add("hidden-el");
    });
    btnSave.addEventListener("click", async function() {
      var id = document.getElementById("taskEditingId").value || uuid4();
      var title = document.getElementById("taskTitleInput").value.trim();
      var prompt = document.getElementById("taskPromptInput").value.trim();
      if (!title) { showToast("⚠️ Vui lòng nhập tên nhiệm vụ."); return; }
      try {
        await libsqlExec(
          "INSERT INTO muse_tasks(id,title,prompt,status,updated_at) VALUES(?,?,?,'pending',?) ON CONFLICT(id) DO UPDATE SET title=excluded.title, prompt=excluded.prompt, updated_at=excluded.updated_at",
          [id, title, prompt, nowISO()]
        );
        document.getElementById("taskEditorCard").classList.add("hidden-el");
        showToast("✅ Đã lưu nhiệm vụ vào libSQL.");
        loadTasksList(currentTaskFilter);
      } catch(e) { showToast("❌ " + e.message); }
    });

    // Filter buttons
    document.querySelectorAll(".task-filter-btn").forEach(function(btn) {
      btn.addEventListener("click", function() {
        document.querySelectorAll(".task-filter-btn").forEach(function(b){ b.classList.remove("active"); });
        btn.classList.add("active");
        currentTaskFilter = btn.getAttribute("data-filter") || "all";
        loadTasksList(currentTaskFilter);
      });
    });
  }

  async function loadTasksList(filter) {
    var listEl = document.getElementById("tasksList");
    var emptyEl = document.getElementById("tasksEmptyState");
    if (!listEl) return;
    // Loading spinner
    listEl.innerHTML = '<div style="text-align:center;padding:24px;color:#94a3b8;font-size:13px;">⏳ Đang tải nhiệm vụ...</div>';
    if (emptyEl) emptyEl.style.display = "none";
    try {
      var sql, params;
      if (!filter || filter === "all") {
        sql = "SELECT id,title,prompt,status,result_summary,updated_at FROM muse_tasks ORDER BY updated_at DESC LIMIT 100";
        params = [];
      } else {
        sql = "SELECT id,title,prompt,status,result_summary,updated_at FROM muse_tasks WHERE status=? ORDER BY updated_at DESC LIMIT 100";
        params = [filter];
      }
      var result = await libsqlExec(sql, params);
      var rows = libsqlRows(result);
      if (rows.length === 0) {
        if (emptyEl) emptyEl.style.display = "";
        listEl.innerHTML = "";
        return;
      }
      if (emptyEl) emptyEl.style.display = "none";
      listEl.innerHTML = "";
      rows.forEach(function(task) {
        var statusMap = { pending:"⏳ Chờ", running:"🔄 Đang chạy", done:"✅ Hoàn thành", error:"❌ Lỗi" };
        var statusLabel = statusMap[task.status] || task.status;
        var card = document.createElement("div");
        card.className = "result-card";
        card.innerHTML =
          '<div style="display:flex;justify-content:space-between;align-items:flex-start;gap:10px;">' +
            '<div class="result-card-title">☑️ ' + escapeHtml(task.title||"Nhiệm vụ") + '</div>' +
            '<span class="task-status-badge ' + escapeHtml(task.status||"pending") + '">' + statusLabel + '</span>' +
          '</div>' +
          (task.prompt ? '<div class="result-card-snippet" style="margin-top:6px;">' + escapeHtml((task.prompt||"").slice(0,200)) + '</div>' : '') +
          (task.result_summary ? '<div class="result-card-snippet" style="margin-top:6px;color:#047857;"><strong>Kết quả:</strong> ' + escapeHtml((task.result_summary||"").slice(0,300)) + '</div>' : '') +
          '<div class="result-card-meta">Cập nhật: ' + escapeHtml(task.updated_at||"") + '</div>' +
          '<div class="result-card-actions">' +
            (task.status === "pending" ? '<button type="button" class="muse-solid-btn" style="font-size:12px;min-height:28px;" data-action="run-task" data-id="' + escapeHtml(task.id) + '" data-prompt="' + escapeHtml(task.prompt||"") + '">▶️ Gửi cho Mart AI</button>' : '') +
            (task.prompt ? '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;" data-action="chat-task" data-prompt="' + escapeHtml(task.prompt||"") + '">💬 Mở trong Chat</button>' : '') +
            (task.status === "done" ? '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;" data-action="reset-task" data-id="' + escapeHtml(task.id) + '">🔁 Chạy lại</button>' : '') +
            '<button type="button" class="muse-outline-btn" style="font-size:12px;min-height:28px;color:#dc2626;border-color:#dc2626;" data-action="del-task" data-id="' + escapeHtml(task.id) + '">🗑️ Xóa</button>' +
          '</div>';
        listEl.appendChild(card);
      });

      // Gửi task cho Mart AI qua /api/tasks/run (endpoint riêng có progress tracking)
      listEl.querySelectorAll("[data-action='run-task']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          var id = btn.getAttribute("data-id");
          var prompt = btn.getAttribute("data-prompt");
          if (!prompt) { showToast("⚠️ Nhiệm vụ này không có lệnh yêu cầu."); return; }
          try {
            await libsqlExec("UPDATE muse_tasks SET status='running', updated_at=? WHERE id=?", [nowISO(), id]);
            showToast("🔄 Đang gửi nhiệm vụ cho Mart AI...");
            loadTasksList(currentTaskFilter);

            var runRes = await apiRequest("/api/tasks/run", {
              method: "POST",
              body: { task_id: id, prompt: prompt }
            });
            if (runRes && runRes.browser_relay) {
              runRes = await executeBrowserRelayIfNeeded(runRes);
              var taskReply = (runRes && (runRes.reply || runRes.content)) || "";
              var taskSum = taskReply.length > 1000 ? taskReply.slice(0, 997) + "..." : taskReply;
              await libsqlExec(
                "UPDATE muse_tasks SET status='done', result_summary=?, updated_at=? WHERE id=?",
                [taskSum, nowISO(), id]
              );
            }
            showToast("✅ Nhiệm vụ hoàn thành! Xem kết quả bên dưới.");
            loadTasksList(currentTaskFilter);
          } catch(e) {
            await libsqlExec(
              "UPDATE muse_tasks SET status='error', result_summary=?, updated_at=? WHERE id=?",
              [e.message, nowISO(), id]
            ).catch(function(){});
            showToast("❌ Lỗi: " + e.message);
            loadTasksList(currentTaskFilter);
          }
        });
      });

      // Mở trong Chat
      listEl.querySelectorAll("[data-action='chat-task']").forEach(function(btn) {
        btn.addEventListener("click", function() {
          var prompt = btn.getAttribute("data-prompt");
          switchMainView("chat");
          var input = document.getElementById("chatPromptInput");
          if (input) { input.value = prompt; input.dispatchEvent(new Event("input")); input.focus(); }
          showToast("📋 Đã chèn lệnh vào Chat. Nhấn Enter để gửi.");
        });
      });

      // Chạy lại
      listEl.querySelectorAll("[data-action='reset-task']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          var id = btn.getAttribute("data-id");
          try {
            await libsqlExec("UPDATE muse_tasks SET status='pending', result_summary='', updated_at=? WHERE id=?", [nowISO(), id]);
            showToast("🔁 Đã đặt lại trạng thái nhiệm vụ.");
            loadTasksList(currentTaskFilter);
          } catch(e) { showToast("❌ " + e.message); }
        });
      });

      // Xóa task
      listEl.querySelectorAll("[data-action='del-task']").forEach(function(btn) {
        btn.addEventListener("click", async function() {
          if (!confirm("Xóa nhiệm vụ này?")) return;
          var id = btn.getAttribute("data-id");
          try {
            await libsqlExec("DELETE FROM muse_tasks WHERE id=?", [id]);
            showToast("✅ Đã xóa nhiệm vụ.");
            loadTasksList(currentTaskFilter);
          } catch(e) { showToast("❌ " + e.message); }
        });
      });
    } catch(e) { if (listEl) listEl.innerHTML = "<p style='color:#dc2626;'>Lỗi tải nhiệm vụ: " + escapeHtml(e.message) + "</p>"; }
  }

  /* ====================================================================
     🔧 FIX 2: Auto-extract artifact từ reply Muse → lưu vào muse_documents
     Quét code blocks (```...```) và section markdown dài > 200 ký tự
     ==================================================================== */
  async function autoExtractDocuments(replyText, threadId) {
    if (!replyText || replyText.length < 100) return;
    var extracted = [];
    // Trích xuất code blocks
    var codeRegex = /```(\w*)\n([\s\S]+?)```/g;
    var match;
    var codeIdx = 1;
    while ((match = codeRegex.exec(replyText)) !== null) {
      var lang = match[1] || "text";
      var code = match[2].trim();
      if (code.length < 40) continue; // Bỏ qua code quá ngắn
      extracted.push({
        title: "Code " + lang.toUpperCase() + " #" + codeIdx + " — " + nowISO().slice(0,10),
        content: "```" + lang + "\n" + code + "\n```"
      });
      codeIdx++;
    }
    // Trích xuất đoạn văn dài > 400 ký tự không có code block
    var plainText = replyText.replace(/```[\s\S]*?```/g, "").trim();
    if (plainText.length > 400) {
      // Lấy tiêu đề từ dòng đầu tiên có nội dung
      var firstLine = plainText.split("\n").map(function(l){ return l.replace(/^#+\s*/, "").trim(); }).filter(Boolean)[0] || "Tài liệu";
      if (firstLine.length > 60) firstLine = firstLine.slice(0, 60) + "...";
      extracted.push({
        title: firstLine + " — " + nowISO().slice(0, 10),
        content: plainText
      });
    }
    if (extracted.length === 0) return;
    // Lưu vào muse_documents (tránh trùng lặp bằng cách check title + ngày)
    for (var i = 0; i < extracted.length; i++) {
      var item = extracted[i];
      try {
        await libsqlExec(
          "INSERT INTO muse_documents(id,title,content,source_thread_id,updated_at) VALUES(?,?,?,?,?)",
          [uuid4(), item.title, item.content, threadId || "", nowISO()]
        );
      } catch(_) { /* Bỏ qua lỗi insert trùng */ }
    }
    if (extracted.length > 0) {
      showToast("📄 Đã lưu " + extracted.length + " tài liệu vào panel Tài liệu.");
    }
  }

  /* ====================================================================
     🔧 FIX 4: Phân trang chat_messages — load theo offset/limit
     ==================================================================== */
  var chatPageState = { threadId: "", offset: 0, limit: 50, total: 0 };

  async function loadThread(threadId, offset) {
    if (!threadId) return;
    state.activeThreadId = threadId;
    var existing = state.threads.find(function (t) { return t.id === threadId; });
    if (!existing) {
      await loadThreads();
      state.activeThreadId = threadId;
    }
    if (typeof offset === "number") {
      await loadThreadMessagesPage(threadId, offset);
    }
    renderTodayActivityLists();
    renderActiveThreadMessages();
  }

  async function loadThreadMessagesPage(threadId, offset) {
    if (!threadId) return;
    offset = offset || 0;
    var resp = await fetch("/api/threads/messages?thread_id=" + encodeURIComponent(threadId) + "&limit=50&offset=" + offset);
    if (!resp.ok) return;
    var data = await resp.json();
    chatPageState.threadId = threadId;
    chatPageState.offset = offset;
    // Đếm tổng từ response
    if (data.total && data.total.rows && data.total.rows[0]) {
      chatPageState.total = parseInt(data.total.rows[0][0].value || "0", 10);
    }
    return data;
  }

  // Thêm nút phân trang vào viewport chat khi thread dài
  function renderChatPagination(viewport) {
    var existing = document.getElementById("chatPaginationBar");
    if (existing) existing.remove();
    if (chatPageState.total <= chatPageState.limit) return; // Không cần phân trang
    var bar = document.createElement("div");
    bar.id = "chatPaginationBar";
    bar.style.cssText = "display:flex;justify-content:center;align-items:center;gap:8px;padding:10px 0;font-size:13px;color:#64748b;";
    var totalPages = Math.ceil(chatPageState.total / chatPageState.limit);
    var currentPage = Math.floor(chatPageState.offset / chatPageState.limit) + 1;
    var prevBtn = document.createElement("button");
    prevBtn.type = "button";
    prevBtn.className = "muse-outline-btn";
    prevBtn.style.cssText = "min-height:28px;font-size:12px;padding:0 10px;";
    prevBtn.textContent = "← Trước";
    prevBtn.disabled = currentPage <= 1;
    prevBtn.addEventListener("click", function() {
      var newOffset = Math.max(0, chatPageState.offset - chatPageState.limit);
      loadThread(chatPageState.threadId, newOffset);
    });
    var pageInfo = document.createElement("span");
    pageInfo.textContent = "Trang " + currentPage + "/" + totalPages + " (" + chatPageState.total + " tin nhắn)";
    var nextBtn = document.createElement("button");
    nextBtn.type = "button";
    nextBtn.className = "muse-outline-btn";
    nextBtn.style.cssText = "min-height:28px;font-size:12px;padding:0 10px;";
    nextBtn.textContent = "Sau →";
    nextBtn.disabled = currentPage >= totalPages;
    nextBtn.addEventListener("click", function() {
      var newOffset = chatPageState.offset + chatPageState.limit;
      loadThread(chatPageState.threadId, newOffset);
    });
    bar.appendChild(prevBtn);
    bar.appendChild(pageInfo);
    bar.appendChild(nextBtn);
    if (viewport) viewport.insertBefore(bar, viewport.firstChild);
  }

  /* ====================================================================
     📚 PANEL LIBRARY — Thư viện Giả liệu (https://muse.ai/library/artifacts)
     ==================================================================== */
  var currentLibCategory = "all";
  var currentLibSortDesc = true;
  var currentLibSelectMode = false;

  function wireLibraryPanel() {
    var topAvatar = document.getElementById("railTopAvatarBtn");
    if (topAvatar) {
      topAvatar.addEventListener("click", function () {
        switchMainView("chat");
      });
    }

    var navItems = document.querySelectorAll("#panelLibrary .lib-nav-item[data-lib-cat]");
    navItems.forEach(function (item) {
      item.addEventListener("click", function () {
        var cat = item.getAttribute("data-lib-cat") || "all";
        navItems.forEach(function (b) { b.classList.remove("active"); });
        item.classList.add("active");
        currentLibCategory = cat;
        var titleEl = document.getElementById("libMainHeading");
        if (titleEl) {
          titleEl.textContent = item.getAttribute("data-lib-title") || "Tất cả các artifact";
        }
        loadLibraryArtifacts();
      });
    });

    var searchInp = document.getElementById("libSearchInput");
    if (searchInp) {
      searchInp.addEventListener("input", function () {
        loadLibraryArtifacts();
      });
    }

    var btnCreate = document.getElementById("btnLibCreateArtifact");
    var creatorCard = document.getElementById("libCreatorCard");
    var btnCancel = document.getElementById("btnLibCancelArtifact");
    var btnSave = document.getElementById("btnLibSaveArtifact");

    if (btnCreate && creatorCard) {
      btnCreate.addEventListener("click", function () {
        creatorCard.classList.toggle("hidden-el");
        var tInp = document.getElementById("libNewTitleInput");
        if (tInp && !creatorCard.classList.contains("hidden-el")) tInp.focus();
      });
    }
    if (btnCancel && creatorCard) {
      btnCancel.addEventListener("click", function () {
        creatorCard.classList.add("hidden-el");
      });
    }
    if (btnSave && creatorCard) {
      btnSave.addEventListener("click", async function () {
        var title = (document.getElementById("libNewTitleInput").value || "").trim();
        var content = (document.getElementById("libNewContentInput").value || "").trim();
        if (!title || !content) {
          showToast("Vui lòng nhập tên và nội dung đối tượng.");
          return;
        }
        try {
          await libsqlExec(
            "INSERT INTO muse_documents(id,title,content,updated_at) VALUES(?,?,?,?)",
            [uuid4(), title, content, nowISO()]
          );
          document.getElementById("libNewTitleInput").value = "";
          document.getElementById("libNewContentInput").value = "";
          creatorCard.classList.add("hidden-el");
          loadLibraryArtifacts();
        } catch (e) {
          showToast("Lỗi lưu đối tượng: " + e.message);
        }
      });
    }

    var btnSort = document.getElementById("btnLibSortOrder");
    if (btnSort) {
      btnSort.addEventListener("click", function () {
        currentLibSortDesc = !currentLibSortDesc;
        loadLibraryArtifacts();
      });
    }

    var btnSelect = document.getElementById("btnLibSelectMode");
    if (btnSelect) {
      btnSelect.addEventListener("click", function () {
        currentLibSelectMode = !currentLibSelectMode;
        btnSelect.textContent = currentLibSelectMode ? "Xong" : "Chọn";
        loadLibraryArtifacts();
      });
    }
  }

  async function loadLibraryArtifacts() {
    var emptyEl = document.getElementById("libEmptyState");
    var gridEl = document.getElementById("libArtifactsGrid");
    var searchInp = document.getElementById("libSearchInput");
    if (!emptyEl || !gridEl) return;

    try {
      var q = searchInp ? searchInp.value.trim().toLowerCase() : "";
      var orderDir = currentLibSortDesc ? "DESC" : "ASC";
      var res = await libsqlExec(
        "SELECT id, title, content, updated_at FROM muse_documents ORDER BY updated_at " + orderDir + " LIMIT 100"
      );
      var rows = libsqlRows(res).filter(function (doc) {
        var title = String(doc.title || "").toLowerCase();
        var content = String(doc.content || "").toLowerCase();
        if (q && title.indexOf(q) === -1 && content.indexOf(q) === -1) return false;
        if (currentLibCategory === "all") return true;
        if (currentLibCategory === "doc") return content.indexOf("http") !== 0;
        if (currentLibCategory === "web") return content.indexOf("http") !== -1 || content.indexOf("<html") !== -1;
        if (currentLibCategory === "image") return content.indexOf("data:image") !== -1 || title.indexOf("ảnh") !== -1;
        if (currentLibCategory === "video") return title.indexOf("video") !== -1;
        if (currentLibCategory === "podcast") return title.indexOf("podcast") !== -1 || title.indexOf("audio") !== -1;
        if (currentLibCategory === "system") return title.indexOf("code") !== -1 || title.indexOf("system") !== -1;
        return true;
      });

      if (rows.length === 0) {
        emptyEl.classList.remove("hidden-el");
        gridEl.classList.add("hidden-el");
        gridEl.innerHTML = "";
        return;
      }

      emptyEl.classList.add("hidden-el");
      gridEl.classList.remove("hidden-el");
      gridEl.innerHTML = "";

      rows.forEach(function (doc) {
        var card = document.createElement("div");
        card.className = "result-card";
        card.innerHTML =
          '<div class="result-card-title">' + escapeHtml(doc.title || "Đối tượng") + '</div>' +
          '<div class="result-card-snippet">' + escapeHtml(String(doc.content || "").slice(0, 160)) + '</div>' +
          '<div class="result-card-meta">' + escapeHtml(doc.updated_at || "") + '</div>' +
          (currentLibSelectMode
            ? '<div class="result-card-actions"><button type="button" class="lib-pill-gray-btn" style="color:#dc2626;" data-del-lib="' + escapeHtml(doc.id) + '">Xóa</button></div>'
            : '');
        gridEl.appendChild(card);
      });

      gridEl.querySelectorAll("[data-del-lib]").forEach(function (btn) {
        btn.addEventListener("click", async function (ev) {
          ev.stopPropagation();
          var id = btn.getAttribute("data-del-lib");
          await libsqlExec("DELETE FROM muse_documents WHERE id=?", [id]);
          loadLibraryArtifacts();
        });
      });
    } catch (_) {
      emptyEl.classList.remove("hidden-el");
      gridEl.classList.add("hidden-el");
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initSession);
  } else {
    initSession();
  }

  // Expose hàm cần dùng trong HTML inline onclick
  window.switchMainView = switchMainView;
})();
