export function makeQR(text: string, scale = 4): HTMLCanvasElement {
  const size = 21;
  const cells: boolean[][] = [];
  for (let i = 0; i < size; i++) {
    cells.push([]);
    for (let j = 0; j < size; j++) cells[i].push(false);
  }

  function fillFinder(r: number, c: number) {
    for (let i = 0; i < 7; i++)
      for (let j = 0; j < 7; j++) {
        const border = i === 0 || i === 6 || j === 0 || j === 6;
        const inner = i >= 2 && i <= 4 && j >= 2 && j <= 4;
        cells[r + i][c + j] = border || inner;
      }
  }
  fillFinder(0, 0);
  fillFinder(0, size - 7);
  fillFinder(size - 7, 0);

  for (let i = 8; i < size - 8; i++) {
    cells[6][i] = i % 2 === 0;
    cells[i][6] = i % 2 === 0;
  }

  const data: boolean[] = [];
  const bytes = new TextEncoder().encode(text);
  data.push(false, true, false, false);
  const len = bytes.length;
  for (let b = 7; b >= 0; b--) data.push(((len >> b) & 1) === 1);
  for (let k = 0; k < bytes.length; k++)
    for (let b = 7; b >= 0; b--) data.push(((bytes[k] >> b) & 1) === 1);
  let rem = 152 - data.length;
  for (let i = 0; i < Math.min(rem, 4); i++) data.push(false);
  while (data.length % 8 !== 0) data.push(false);
  const padBytes = [0xec, 0x11];
  let pi = 0;
  while (data.length < 152) {
    for (let b = 7; b >= 0; b--) data.push(((padBytes[pi] >> b) & 1) === 1);
    pi = (pi + 1) % 2;
  }

  let di = 0;
  let col = size - 1;
  let up = true;
  while (col > 0) {
    if (col === 6) col--;
    if (up) {
      for (let r = size - 1; r >= 0; r--)
        for (let c = col; c >= col - 1; c--) {
          if (!cells[r] || cells[r][c] !== false) continue;
          if (di < data.length) { cells[r][c] = data[di]; di++; }
        }
    } else {
      for (let r = 0; r < size; r++)
        for (let c = col; c >= col - 1; c--) {
          if (!cells[r] || cells[r][c] !== false) continue;
          if (di < data.length) { cells[r][c] = data[di]; di++; }
        }
    }
    up = !up;
    col -= 2;
  }

  const formatBits = [true, false, true, false, true, false, false, false, false, false, true, false, false, true, false];
  for (let i = 0; i < 6; i++) cells[i][8] = formatBits[i];
  for (let i = 6; i < 8; i++) cells[i][8] = formatBits[i];
  for (let i = 0; i < 6; i++) cells[8][size - 1 - i] = formatBits[i];
  for (let i = 6; i < 8; i++) cells[8][size - 1 - i] = formatBits[i];
  cells[7][8] = formatBits[6]; cells[8][8] = formatBits[7]; cells[8][7] = formatBits[8];
  for (let i = 0; i < 7; i++) cells[size - 1 - i][8] = formatBits[8 + i];
  cells[size - 8][8] = true;

  const margin = 4;
  const canvasSize = (size + 2 * margin) * scale;
  const canvas = document.createElement('canvas');
  canvas.width = canvasSize;
  canvas.height = canvasSize;
  canvas.style.width = canvasSize + 'px';
  canvas.style.height = canvasSize + 'px';
  const ctx = canvas.getContext('2d')!;
  ctx.fillStyle = '#fff';
  ctx.fillRect(0, 0, canvasSize, canvasSize);
  ctx.fillStyle = '#000';
  for (let r = 0; r < size; r++)
    for (let c = 0; c < size; c++)
      if (cells[r][c]) ctx.fillRect((margin + c) * scale, (margin + r) * scale, scale, scale);
  return canvas;
}
