// Voice for the aimux web client: dictation through the browser's speech
// recognition (on iPhone it runs through Apple's speech service, like
// keyboard dictation) and replies read aloud with the device's own voices.
// Approvals are never answered by voice; the page only reads them out.
'use strict';

const Voice = (() => {
  const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  const synth = window.speechSynthesis;
  const KEY = 'aimux.voice';

  // Settings: speak replies, send when the user stops talking, and a
  // hands-free loop (listen, send, speak the reply, listen again). Stored
  // per device; a private window just starts with them off.
  const defaults = { speak: false, autoSend: false, handsFree: false };
  let settings = { ...defaults };
  try { settings = { ...defaults, ...JSON.parse(localStorage.getItem(KEY) || '{}') }; } catch (_) { /* no storage */ }

  function set(name, value) {
    settings[name] = value;
    if (name === 'handsFree' && value) { settings.speak = true; settings.autoSend = true; }
    try { localStorage.setItem(KEY, JSON.stringify(settings)); } catch (_) { /* no storage */ }
  }

  let rec = null;

  // listen streams what the user says: onText(text) with each interim
  // result, onEnd(finalText) once they stop. Returns false if it cannot.
  function listen({ onText, onEnd, onError }) {
    if (!Recognition) return false;
    stopSpeaking();
    stopListening();
    // Each recognizer clears the shared one only if it is still current: an
    // old one ending late must not orphan a newer recording.
    const r = new Recognition();
    rec = r;
    r.lang = navigator.language || 'en-US';
    r.interimResults = true;
    r.continuous = false; // one utterance; ends when the user pauses
    let final = '';
    let latest = '';
    r.onresult = (e) => {
      let interim = '';
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const r = e.results[i];
        if (r.isFinal) final += r[0].transcript;
        else interim += r[0].transcript;
      }
      latest = (final + interim).trim();
      onText(latest);
    };
    r.onerror = (e) => { if (onError) onError(e.error || 'error'); };
    r.onend = () => {
      if (rec === r) rec = null;
      onEnd((final || latest).trim());
    };
    try {
      r.start();
    } catch (_) {
      if (rec === r) rec = null;
      return false;
    }
    return true;
  }

  function stopListening() {
    if (rec) { try { rec.stop(); } catch (_) { /* already stopped */ } }
  }

  function listening() { return rec !== null; }

  // spoken turns a reply into something worth hearing: no code, no
  // markdown symbols, links as their text, and not too long.
  function spoken(reply, toolSummary) {
    let t = (reply || '')
      .replace(/```[\s\S]*?(```|$)/g, ' (code omitted) ')
      .replace(/`([^`]+)`/g, '$1')
      .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
      .replace(/^\s*[-*]\s+/gm, '')
      .replace(/[#*_>|]+/g, ' ')
      .replace(/\s+/g, ' ')
      .trim();
    if (t.length > 700) {
      const cut = t.lastIndexOf('. ', 700);
      t = t.slice(0, cut > 200 ? cut + 1 : 700) + ' The rest is on screen.';
    }
    return [toolSummary, t].filter(Boolean).join(' ');
  }

  // speakGen tells a finished reply from an interrupted one: onEnd runs
  // only when the speech played out, not when it was cancelled (the user
  // tapped the mic, or sent something), which must not restart listening.
  let speakGen = 0;

  function speak(text, onEnd) {
    if (!synth || !text) { if (onEnd) onEnd(); return; }
    stopSpeaking();
    const gen = ++speakGen;
    const u = new SpeechSynthesisUtterance(text);
    u.lang = navigator.language || 'en-US';
    const done = () => { if (gen === speakGen && onEnd) onEnd(); };
    u.onend = done;
    u.onerror = done;
    synth.speak(u);
  }

  function stopSpeaking() {
    speakGen++;
    if (synth && (synth.speaking || synth.pending)) synth.cancel();
  }

  return {
    canListen: !!Recognition,
    canSpeak: !!synth,
    settings: () => ({ ...settings }),
    set, listen, stopListening, listening, spoken, speak, stopSpeaking,
  };
})();
