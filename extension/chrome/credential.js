(function (root) {
  "use strict";

  const MAX_CREDENTIAL_BYTES = 16 * 1024;

  function parseNotificationURL(rawURL) {
    let parsed;
    try {
      parsed = new URL(rawURL);
    } catch (_) {
      return null;
    }
    if (parsed.protocol !== "https:" || parsed.hostname !== "sb.sb") return null;
    const match = /^\/u\/(\d+)\/$/.exec(parsed.pathname);
    if (!match || parsed.searchParams.get("tab") !== "notifications") return null;
    return { forumUserID: match[1], url: parsed.href, hostname: parsed.hostname, pathname: parsed.pathname };
  }

  function domainMatches(host, domain) {
    const normalized = String(domain || "").replace(/^\./, "").toLowerCase();
    const current = String(host || "").toLowerCase();
    return current === normalized || current.endsWith("." + normalized);
  }

  function buildCookieHeader(cookies, page) {
    return cookies
      .filter((cookie) => cookie && cookie.name && domainMatches(page.hostname, cookie.domain || page.hostname))
      .filter((cookie) => page.pathname.startsWith(cookie.path || "/"))
      .filter((cookie) => !cookie.secure || page.url.startsWith("https://"))
      .sort((a, b) => (b.path || "/").length - (a.path || "/").length || a.name.localeCompare(b.name))
      .map((cookie) => cookie.name + "=" + (cookie.value || ""))
      .join("; ");
  }

  function utf8Base64URL(value) {
    const bytes = new TextEncoder().encode(value);
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
  }

  function encodeCredential(forumUserID, cookie) {
    if (!/^\d+$/.test(forumUserID) || !cookie || /[\r\n\0]/.test(cookie)) throw new Error("凭据字段无效");
    const value = "SBM1." + utf8Base64URL(JSON.stringify({ v: 1, forum_user_id: forumUserID, cookie }));
    if (new TextEncoder().encode(value).length > MAX_CREDENTIAL_BYTES) throw new Error("凭据超过 16 KiB，无法使用");
    return value;
  }

  const api = { MAX_CREDENTIAL_BYTES, parseNotificationURL, buildCookieHeader, encodeCredential };
  root.SBMCredential = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})(typeof globalThis !== "undefined" ? globalThis : this);
