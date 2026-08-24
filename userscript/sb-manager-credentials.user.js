// ==UserScript==
// @name         sb-manager-bot 凭据助手
// @namespace    https://github.com/krabdo/sb-manager-bot
// @version      0.1.0
// @description  在本地生成 sb-manager-bot 绑定凭据，不发送任何网络请求
// @match        https://sb.sb/u/*
// @grant        GM.cookie
// @grant        GM_cookie
// @grant        GM.setClipboard
// @grant        GM_setClipboard
// @run-at       document-idle
// ==/UserScript==

(function (root) {
  "use strict";

  function extractUserID(locationLike) {
    const match = /^\/u\/(\d+)\/$/.exec(locationLike.pathname);
    const params = new URLSearchParams(locationLike.search || "");
    return match && params.get("tab") === "notifications" ? match[1] : null;
  }

  function utf8Base64URL(value) {
    const bytes = new TextEncoder().encode(value);
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
  }

  function encodeCredential(forumUserID, cookie) {
    if (!/^\d+$/.test(forumUserID) || !cookie || /[\r\n\0]/.test(cookie)) throw new Error("凭据字段无效");
    return "SBM1." + utf8Base64URL(JSON.stringify({ v: 1, forum_user_id: forumUserID, cookie }));
  }

  function domainMatches(host, domain) {
    const normalized = String(domain || "").replace(/^\./, "").toLowerCase();
    return host.toLowerCase() === normalized || host.toLowerCase().endsWith("." + normalized);
  }

  function buildCookieHeader(cookies, locationLike) {
    const path = locationLike.pathname || "/";
    const selected = new Map();
    cookies
      .filter((cookie) => cookie && cookie.name && domainMatches(locationLike.hostname, cookie.domain || locationLike.hostname))
      .filter((cookie) => path.startsWith(cookie.path || "/"))
      .sort((a, b) => (b.path || "/").length - (a.path || "/").length)
      .forEach((cookie) => { if (!selected.has(cookie.name)) selected.set(cookie.name, cookie.value || ""); });
    return Array.from(selected, ([name, value]) => name + "=" + value).join("; ");
  }

  async function listCookies() {
    const modern = typeof GM !== "undefined" && GM.cookie && GM.cookie.list;
    const legacy = typeof GM_cookie !== "undefined" && GM_cookie.list;
    const list = modern ? GM.cookie.list.bind(GM.cookie) : legacy ? GM_cookie.list.bind(GM_cookie) : null;
    if (!list) return [];
    return await new Promise((resolve, reject) => {
      let settled = false;
      const callback = (cookies, error) => { settled = true; error ? reject(error) : resolve(cookies || []); };
      try {
        const result = list({ url: location.href }, callback);
        if (result && typeof result.then === "function") result.then((cookies) => { if (!settled) resolve(cookies || []); }, reject);
      } catch (error) { reject(error); }
    });
  }

  async function copyText(text) {
    if (typeof GM !== "undefined" && GM.setClipboard) return GM.setClipboard(text, "text");
    if (typeof GM_setClipboard !== "undefined") return GM_setClipboard(text, "text");
    return navigator.clipboard.writeText(text);
  }

  function manualCookie() {
    return window.prompt("稳定版 Tampermonkey 通常无法读取 HttpOnly 会话 Cookie。请打开开发者工具 → Network，刷新通知页，选择该页面请求，在 Request Headers 中复制 Cookie 的完整值（不要包含“Cookie:”前缀），粘贴到这里。内容只在本页本地处理：", "") || "";
  }

  async function generate(userID) {
    let cookie = "";
    try {
      const cookies = await listCookies();
      if (cookies.some((item) => item && item.httpOnly)) cookie = buildCookieHeader(cookies, location);
    } catch (_) { /* use local fallback */ }
    if (!cookie) cookie = manualCookie().trim();
    if (!cookie || /^cookie\s*:/i.test(cookie) || /[\r\n\0]/.test(cookie)) throw new Error("未获得有效的完整 Cookie");
    const credential = encodeCredential(userID, cookie);
    if (credential.length > 16 * 1024) throw new Error("凭据超过 16 KiB，无法使用");
    await copyText(credential);
    window.alert("Bot 凭据已复制。请立即粘贴到 Bot 私聊；绑定成功后清除剪贴板历史。不要把它发给任何其他人。\n\n注意：Base64URL 仅用于封装，不是加密。");
  }

  function init() {
    const userID = extractUserID(location);
    if (!userID || !document.querySelector('a.tab[href*="tab=notifications"]')) return;
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = "复制 Bot 凭据";
    button.style.cssText = "position:fixed;right:20px;bottom:20px;z-index:2147483647;padding:10px 16px;border:0;border-radius:8px;background:#229ed9;color:white;font-weight:600;cursor:pointer;box-shadow:0 2px 10px #0004";
    button.addEventListener("click", async () => { button.disabled = true; try { await generate(userID); } catch (error) { window.alert(error.message || "生成失败"); } finally { button.disabled = false; } });
    document.body.appendChild(button);
  }

  const api = { extractUserID, encodeCredential, buildCookieHeader, listCookies };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else init();
})(typeof globalThis !== "undefined" ? globalThis : this);
