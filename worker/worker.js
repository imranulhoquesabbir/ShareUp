// shareUp relay (Cloudflare Worker, free plan).
// Keeps ONE fixed link per PC:  https://<your-worker>.workers.dev/d/<ID>
// The PC tells this Worker its current addresses; the Worker sends each phone to the right one:
//   same network as the PC  -> the PC's local address (fast, no internet needed by the PC)
//   anywhere else           -> the PC's current internet tunnel
// Needs one KV namespace bound to this Worker with the variable name  SU  (see WORKER_SETUP.md).

const enc = new TextEncoder();
const hdr = (type) => ({ 'content-type': type, 'cache-control': 'no-store' });
const J = (o, s = 200) => new Response(JSON.stringify(o), { status: s, headers: hdr('application/json') });
const H = (b, s = 200) => new Response(b, { status: s, headers: hdr('text/html; charset=utf-8') });
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

async function sha(s) {
  const b = await crypto.subtle.digest('SHA-256', enc.encode(s));
  return [...new Uint8Array(b)].map((x) => x.toString(16).padStart(2, '0')).join('');
}

const LAN_RE = /^http:\/\/(10\.\d{1,3}|172\.(1[6-9]|2\d|3[01])|192\.168)\.\d{1,3}\.\d{1,3}:\d{2,5}$/;
const TUN_RE = /^https:\/\/[a-z0-9-]+\.trycloudflare\.com\/\?k=[A-Za-z0-9]+$/;

const STYLE = `<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>shareUp</title>
<style>:root{color-scheme:light dark}body{font-family:system-ui,sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0;padding:24px;box-sizing:border-box;background:#0f1115;color:#e8eaed}
.b{max-width:340px;width:100%;text-align:center}h2{margin:0 0 8px}p{color:#9aa0aa;line-height:1.5}
a.btn{display:block;margin:10px 0;padding:14px;border-radius:14px;background:linear-gradient(135deg,#3b82f6,#6366f1);color:#fff;text-decoration:none;font-weight:650}
a.alt{background:#20242d}</style>`;

const page = (title, text, extra = '') => H(`<!doctype html>${STYLE}<div class="b"><h2>${esc(title)}</h2><p>${esc(text)}</p>${extra}</div>`);

function go(target) {
  // Carry this phone's device id to the next address so "Trusted device" keeps working there.
  const t = JSON.stringify(target).replace(/</g, '\\u003c');
  return H(`<!doctype html>${STYLE}<div class="b"><h2>shareUp</h2><p>Connecting… / Connect হচ্ছে…</p><a class="btn" id="a" href="${esc(target)}">Continue</a></div>
<script>(function(){var id='';try{id=localStorage.getItem('su_dev')||''}catch(e){}
if(!/^[A-Za-z0-9]{16,64}$/.test(id)){var a=new Uint8Array(16);crypto.getRandomValues(a);id=Array.from(a,function(x){return('0'+x.toString(16)).slice(-2)}).join('');try{localStorage.setItem('su_dev',id)}catch(e){}}
location.replace(${t}+'#d='+id)})()</script>`);
}

async function alive(base) {
  try {
    const r = await fetch(base, { redirect: 'manual', signal: AbortSignal.timeout(4000) });
    return [200, 301, 302, 401, 403].includes(r.status); // 401 = tunnel is up and asking for the key
  } catch (e) {
    return false;
  }
}

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const ip = req.headers.get('CF-Connecting-IP') || '';
    if (u.pathname === '/ping') return new Response('ok', { headers: hdr('text/plain') });
    const m = u.pathname.match(/^\/(r|d)\/([A-Za-z0-9]{8,32})$/);
    if (!m) return page('shareUp relay', 'This is the shareUp fixed-link helper. Scan the QR code from the shareUp window on your PC.');
    if (!env.SU) return page('Setup needed', 'KV binding "SU" is missing. Follow WORKER_SETUP.md.');
    const [, kind, id] = m;

    // ---- PC -> Worker: "my current addresses are ..." ----
    if (kind === 'r') {
      if (req.method !== 'POST') return J({ error: 'POST only' }, 405);
      let b;
      try { b = await req.json(); } catch (e) { return J({ error: 'bad json' }, 400); }
      if (typeof b.key !== 'string' || b.key.length < 16 || b.key.length > 128) return J({ error: 'bad key' }, 400);
      const kh = await sha(b.key);
      const old = await env.SU.get('d:' + id, 'json');
      if (old && old.kh !== kh) return J({ error: 'key mismatch' }, 403);
      const lan = (Array.isArray(b.lan) ? b.lan : []).filter((x) => typeof x === 'string' && LAN_RE.test(x)).slice(0, 4);
      const tunnel = typeof b.tunnel === 'string' && TUN_RE.test(b.tunnel) ? b.tunnel : '';
      await env.SU.put('d:' + id, JSON.stringify({ kh, lan, tunnel, ip, t: Date.now() }));
      return J({ ok: true, ip });
    }

    // ---- phone scans the fixed QR ----
    const rec = await env.SU.get('d:' + id, 'json');
    if (!rec) return page('Not set up yet', 'Open shareUp on your PC and wait a few seconds, then scan again. PC-তে shareUp চালু করে একটু অপেক্ষা করুন, তারপর আবার scan করুন।');
    const lan = rec.lan && rec.lan[0];
    const base = rec.tunnel ? rec.tunnel.split('/?k=')[0] : '';
    const via = u.searchParams.get('via');

    if (via === 'lan' && lan) return go(lan);
    if (via === 'net' && rec.tunnel) return go(rec.tunnel);
    if (lan && rec.ip && rec.ip === ip) return go(lan); // same network as the PC
    if (rec.tunnel && (await alive(base))) return go(rec.tunnel);

    // could not decide: let the person pick
    const self = u.origin + u.pathname;
    let btns = '';
    if (lan) btns += `<a class="btn" href="${esc(self)}?via=lan">Local network / একই WiFi</a>`;
    if (rec.tunnel) btns += `<a class="btn alt" href="${esc(self)}?via=net">Internet</a>`;
    btns += `<a class="btn alt" href="${esc(self)}">Retry / আবার চেষ্টা</a>`;
    return page('Cannot reach the PC', 'The PC may be off or offline. If your phone is on the same Wi-Fi as the PC, choose Local network. PC বন্ধ বা offline হতে পারে। একই WiFi-তে থাকলে Local network চাপুন।', btns);
  },
};
