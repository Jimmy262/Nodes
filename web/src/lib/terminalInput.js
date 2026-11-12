// Split by Unicode code points, not UTF-16 offsets: emoji must not be cut in
// half. 1000 code points fit both server limits (4096 input bytes, 8192/frame).
export function terminalInputFrames(data) {
  const frames = [];
  let chunk = '',
    count = 0;
  for (const character of data) {
    chunk += character;
    if (++count === 1000) {
      frames.push(JSON.stringify({ type: 'input', data: chunk }));
      chunk = '';
      count = 0;
    }
  }
  if (chunk) frames.push(JSON.stringify({ type: 'input', data: chunk }));
  return frames;
}
