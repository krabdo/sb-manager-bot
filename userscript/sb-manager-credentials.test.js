const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

global.TextEncoder = require("node:util").TextEncoder;
global.btoa = (value) => Buffer.from(value, "binary").toString("base64");
const scriptPath = path.join(__dirname, "sb-manager-credentials.user.js");
const api = require(scriptPath);

test("extracts UID only from notification page", () => {
  assert.equal(api.extractUserID({ pathname: "/u/1777/", search: "?tab=notifications" }), "1777");
  assert.equal(api.extractUserID({ pathname: "/u/1777/", search: "?tab=posts" }), null);
  assert.equal(api.extractUserID({ pathname: "/u/name/", search: "?tab=notifications" }), null);
});

test("encodes UTF-8 JSON as SBM1 base64url", () => {
  const value = api.encodeCredential("1777", "会话=有效; token=a+b/c");
  assert.match(value, /^SBM1\.[A-Za-z0-9_-]+$/);
  const json = JSON.parse(Buffer.from(value.slice(5), "base64url").toString("utf8"));
  assert.deepEqual(json, { v: 1, forum_user_id: "1777", cookie: "会话=有效; token=a+b/c" });
});

test("prefers longest matching cookie path and excludes other domains", () => {
  const cookies = [
    { name: "sid", value: "root", domain: ".sb.sb", path: "/" },
    { name: "sid", value: "user", domain: "sb.sb", path: "/u/" },
    { name: "theme", value: "dark", domain: "sb.sb", path: "/" },
    { name: "evil", value: "x", domain: "example.com", path: "/" },
  ];
  assert.equal(api.buildCookieHeader(cookies, { hostname: "sb.sb", pathname: "/u/1777/" }), "sid=user; theme=dark");
});

test("has no network-capable userscript grant or request API", () => {
  const source = fs.readFileSync(scriptPath, "utf8");
  assert.doesNotMatch(source, /@connect|GM_xmlhttpRequest|XMLHttpRequest|\bfetch\s*\(/);
});

test("reads cookies from modern promise and legacy callback GM APIs", async () => {
  global.location = { href: "https://sb.sb/u/1777/?tab=notifications" };
  global.GM = { cookie: { list: async () => [{ name: "session", value: "modern", httpOnly: true }] } };
  assert.equal((await api.listCookies())[0].value, "modern");
  delete global.GM;
  global.GM_cookie = { list: (_details, callback) => callback([{ name: "session", value: "legacy", httpOnly: true }]) };
  assert.equal((await api.listCookies())[0].value, "legacy");
  delete global.GM_cookie;
  delete global.location;
});
