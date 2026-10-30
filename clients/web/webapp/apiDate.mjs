// Mirrors APIDate.parse in the iPhone app: the server's RFC 3339 timestamps carry 0-9 fractional
// digits, and all of them count. Returns epoch milliseconds as a float (NaN when not a timestamp).
export function apiDate(text) {
  const m = /^([^.]*?)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)$/.exec(text);
  if (!m) return NaN;
  const whole = Date.parse(m[1] + m[3]);
  return m[2] === undefined ? whole : whole + Number('0.' + m[2]) * 1000;
}
