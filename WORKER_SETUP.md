# Fixed QR setup (one time, free)

shareUp-এর fixed QR-এর জন্য একটা ছোট Cloudflare Worker লাগে। Domain কিনতে হয় না, শুধু free Cloudflare account লাগে।

## Steps

1. https://dash.cloudflare.com এ login করুন (free account খুলুন)।
2. **Workers & Pages → Create → Create Worker** চাপুন। নাম দিন, যেমন `shareup`। **Deploy** চাপুন।
3. **Edit code** চাপুন। আগের সব code মুছে এই repo-এর `worker/worker.js` ফাইলের পুরো code paste করুন। **Deploy** চাপুন।
4. KV বানান: **Storage & Databases → KV → Create namespace**, নাম দিন `shareup-kv`।
5. Worker-এ KV জুড়ুন: Worker খুলুন → **Settings → Bindings → Add → KV namespace**
   - Variable name: **`SU`** (ঠিক এই নামটাই, বড় হাতের অক্ষরে)
   - Namespace: `shareup-kv`
   - Save/Deploy চাপুন।
6. Worker-এর URL কপি করুন, দেখতে এরকম: `https://shareup.yourname.workers.dev`
7. shareUp খুলুন → **Settings → Smart connection** → ওই URL paste করে **Save** চাপুন।
8. কিছুক্ষণের মধ্যে Settings-এ **✓ Ready** দেখাবে, আর Connect ট্যাবে fixed QR আসবে।

## Notes
- Free plan-এ দিনে ১ লাখ request আর KV-তে ১০০০ write পাওয়া যায়। shareUp শুধু address বদলালে (বা ১৫ মিনিট পর পর) write করে, তাই যথেষ্ট।
- QR-এ যে ID আছে সেটা এই PC-র। Worker-এর কাছে PC-র private key-র শুধু hash থাকে, তাই অন্য কেউ আপনার QR-এর address বদলাতে পারবে না।
- PC আর phone একই WiFi-তে থাকলে (দুটোর public IP এক হলে) QR সরাসরি local address-এ যায়। না হলে internet tunnel-এ যায়। অনিশ্চিত হলে phone-এ দুটো button দেখায়।
- নতুন QR লাগলে Settings-এ **Reset fixed QR** চাপুন। তখন পুরোনো QR আর কাজ করবে না।

## Shob user-er jonno auto (developer-er jonno, ekbar)
Worker deploy kore URL pele, seta app-er bhitore baniye nao, tahole shob user-er kono setup lagbe na:
1. GitHub repo → **Settings → Secrets and variables → Actions → Variables → New repository variable**
2. Name: `WORKER_URL`, Value: `https://shareup.yourname.workers.dev`
3. Notun build/release chalao. Ei build-er app prothom bar chalu hoyei fixed QR dekhabe.
Kono user Settings-e nijer URL dile seta ei default-er upor priority pabe.
