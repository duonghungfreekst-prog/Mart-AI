/* Mart AI Extension v1.2 — Content Bridge (kết nối trực tiếp không phụ thuộc Extension ID) */
"use strict";

(function () {
  if (window.__MART_AI_CONTENT_BRIDGE_V12__) return;
  window.__MART_AI_CONTENT_BRIDGE_V12__ = true;

  function postToPage(data) {
    try {
      window.postMessage(data, window.location.origin || "*");
    } catch (_) {
      window.postMessage(data, "*");
    }
  }

  // Chuyển tiếp yêu cầu từ trang Web Mart AI sang background service worker của tiện ích
  window.addEventListener("message", function (ev) {
    if (ev.source !== window || !ev.data || ev.data.__mart_ext_req !== true) {
      return;
    }
    var reqId = ev.data.reqId || "";
    var payload = ev.data.payload || {};
    try {
      chrome.runtime.sendMessage(payload, function (resp) {
        if (chrome.runtime.lastError) {
          postToPage({
            __mart_ext_resp: true,
            reqId: reqId,
            result: null,
            error: chrome.runtime.lastError.message || "EXT_ERR",
          });
          return;
        }
        postToPage({
          __mart_ext_resp: true,
          reqId: reqId,
          result: resp || null,
        });
      });
    } catch (err) {
      postToPage({
        __mart_ext_resp: true,
        reqId: reqId,
        result: null,
        error: (err && err.message) || "EXT_CONTEXT_INVALIDATED",
      });
    }
  });

  // Nhận sự kiện chủ động từ background (ví dụ: vừa cài tiện ích xong hoặc vừa đăng nhập muse.ai xong)
  try {
    chrome.runtime.onMessage.addListener(function (msg) {
      if (msg && msg.__mart_bg_event) {
        postToPage({
          __mart_ext_push: true,
          event: msg.event || "",
          cookie: msg.cookie || "",
          hasExistingSession: !!msg.hasExistingSession,
          version: msg.version || "1.2.1",
        });
      }
    });
  } catch (_) {}

  // Ngay khi nạp vào trang Mart AI, kiểm tra phiên muse.ai sẵn có và báo ngay cho trang Web
  try {
    chrome.runtime.sendMessage({ action: "ping_with_cookie" }, function (resp) {
      if (chrome.runtime.lastError || !resp || !resp.ok) return;
      postToPage({
        __mart_ext_push: true,
        event: "MART_EXT_SESSION_READY",
        cookie: resp.cookie || "",
        hasExistingSession: !!resp.hasExistingSession,
        version: resp.version || "1.2.1",
      });
    });
  } catch (_) {}
})();
