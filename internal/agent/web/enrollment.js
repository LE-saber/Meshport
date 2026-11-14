/* QR imports only fill the registration form. The Go core validates the code,
 * HTTPS origin and server key before sending any credentials. No remote decoder. */
'use strict';
window.MeshportEnrollment = (() => {
  let receive = null;
  function acceptScan(value) {
    if (receive) receive(value);
  }
  function bind(fields, error) {
    receive = (value) => {
      try {
        let code = String(value || '').trim();
        if (code.includes('#')) code = code.slice(code.indexOf('#') + 1);
        if (/^mpe_[A-Za-z0-9_-]+$/.test(code)) {
          fields.enroll.value = code; error.textContent = ''; return;
        }
        if (!code.startsWith('mpjoin1.') || code.length > 4096) throw new Error(mpText('不是有效的 Meshport 安全註冊碼'));
        const encoded = code.slice(8).replace(/-/g, '+').replace(/_/g, '/');
        const raw = Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0));
        const invite = JSON.parse(new TextDecoder().decode(raw));
        const server = new URL(invite.server);
        if (invite.v !== 1 || server.protocol !== 'https:' || !invite.pin || !invite.token) throw new Error(mpText('註冊碼資料不完整'));
        fields.enroll.value = code;
        fields.server.value = invite.server;
        error.textContent = mpText('已讀取安全註冊碼，按註冊以驗證伺服器公鑰。');
      } catch (e) { error.textContent = e.message; }
    };
    fields.enroll.addEventListener('change', () => {
      if (fields.enroll.value.includes('mpjoin1.')) acceptScan(fields.enroll.value);
    });
    const file = document.createElement('input');
    file.type = 'file'; file.accept = 'image/*'; file.hidden = true;
    file.addEventListener('change', async () => {
      const imageFile = file.files[0];
      if (!imageFile) return;
      let url;
      try {
        if (imageFile.size > 20 * 1024 * 1024) throw new Error(mpText('圖片太大，請選擇 20 MB 以下的圖片'));
        url = URL.createObjectURL(imageFile);
        const img = new Image(); img.src = url; await img.decode();
        const scale = Math.min(1, 2400 / Math.max(img.width, img.height));
        const canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(img.width * scale)); canvas.height = Math.max(1, Math.round(img.height * scale));
        const ctx = canvas.getContext('2d', { willReadFrequently: true });
        ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
        const data = ctx.getImageData(0, 0, canvas.width, canvas.height);
        const qr = jsQR(data.data, data.width, data.height, { inversionAttempts: 'attemptBoth' });
        if (!qr) throw new Error(mpText('未找到二維碼，請選擇清晰的註冊二維碼圖片'));
        acceptScan(qr.data);
      } catch (e) { error.textContent = e.message; }
      finally { if (url) URL.revokeObjectURL(url); file.value = ''; }
    });
    const row = document.createElement('div'); row.className = 'row';
    const imageButton = document.createElement('button'); imageButton.type = 'button'; imageButton.className = 'btn';
    imageButton.textContent = mpText('匯入二維碼圖片'); imageButton.onclick = () => file.click();
    row.append(imageButton, file);
    if (window.MeshportNative && typeof window.MeshportNative.scanEnrollment === 'function') {
      const camera = document.createElement('button'); camera.type = 'button'; camera.className = 'btn';
      camera.textContent = mpText('相機掃碼'); camera.onclick = () => window.MeshportNative.scanEnrollment(); row.prepend(camera);
    }
    return row;
  }
  return { acceptScan, bind };
})();
