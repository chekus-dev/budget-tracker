/* Passkey (fingerprint / face / device PIN) sign-in and registration. */
(function () {
  'use strict';

  function b64uToBuf(s) {
    s = s.replace(/-/g, '+').replace(/_/g, '/');
    while (s.length % 4) s += '=';
    var bin = atob(s), out = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out.buffer;
  }
  function bufToB64u(buf) {
    var a = new Uint8Array(buf), s = '';
    for (var i = 0; i < a.length; i++) s += String.fromCharCode(a[i]);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }
  function post(url, body) {
    return fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined
    }).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (j) {
        if (!res.ok) throw new Error(j.error || 'Something went wrong. Please try again.');
        return j;
      });
    });
  }

  var supported = !!(window.PublicKeyCredential && navigator.credentials);
  var msg = document.querySelector('[data-passkey-msg]');
  function say(t) { if (msg) msg.textContent = t || ''; }
  function friendly(e) {
    if (e && (e.name === 'NotAllowedError' || e.name === 'AbortError')) return 'Cancelled.';
    return (e && e.message) || 'Something went wrong. Please try again.';
  }

  function register(btn) {
    btn.disabled = true;
    say('');
    post('/passkeys/register/begin').then(function (opts) {
      var pk = opts.publicKey;
      pk.challenge = b64uToBuf(pk.challenge);
      pk.user.id = b64uToBuf(pk.user.id);
      (pk.excludeCredentials || []).forEach(function (c) { c.id = b64uToBuf(c.id); });
      return navigator.credentials.create({ publicKey: pk });
    }).then(function (cred) {
      var r = cred.response;
      return post('/passkeys/register/finish?name=' + encodeURIComponent('This device'), {
        id: cred.id,
        rawId: bufToB64u(cred.rawId),
        type: cred.type,
        authenticatorAttachment: cred.authenticatorAttachment,
        response: {
          attestationObject: bufToB64u(r.attestationObject),
          clientDataJSON: bufToB64u(r.clientDataJSON),
          transports: r.getTransports ? r.getTransports() : []
        },
        clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {}
      });
    }).then(function () {
      say('Done. You can now sign in with your fingerprint on this device.');
    }).catch(function (e) {
      say(friendly(e));
    }).then(function () { btn.disabled = false; });
  }

  function login(btn) {
    btn.disabled = true;
    say('');
    post('/passkeys/login/begin').then(function (opts) {
      var pk = opts.publicKey;
      pk.challenge = b64uToBuf(pk.challenge);
      (pk.allowCredentials || []).forEach(function (c) { c.id = b64uToBuf(c.id); });
      return navigator.credentials.get({ publicKey: pk });
    }).then(function (cred) {
      var r = cred.response;
      return post('/passkeys/login/finish', {
        id: cred.id,
        rawId: bufToB64u(cred.rawId),
        type: cred.type,
        authenticatorAttachment: cred.authenticatorAttachment,
        response: {
          authenticatorData: bufToB64u(r.authenticatorData),
          clientDataJSON: bufToB64u(r.clientDataJSON),
          signature: bufToB64u(r.signature),
          userHandle: r.userHandle ? bufToB64u(r.userHandle) : ''
        },
        clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {}
      });
    }).then(function (j) {
      window.location.href = j.redirect || '/';
    }).catch(function (e) {
      say(friendly(e));
      btn.disabled = false;
    });
  }

  function init() {
    var add = document.querySelector('[data-passkey-add]');
    var signin = document.querySelector('[data-passkey-login]');
    if (!supported) {
      if (add) add.style.display = 'none';
      if (signin) signin.style.display = 'none';
      if (add) say('This browser does not support passkeys.');
      return;
    }
    if (add) add.addEventListener('click', function () { register(add); });
    if (signin) {
      signin.style.display = '';
      signin.addEventListener('click', function () { login(signin); });
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
