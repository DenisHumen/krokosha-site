// Line icons of the design (design/pages/index.dc.html → ICONS): one path each, drawn in a 24×24 box
// with `stroke="currentColor"`, so they follow the colour of the text around them.

export const ICONS: Record<string, string> = {
  telegram: 'M21.5 4.5 3 11.2l6.2 2.3M21.5 4.5l-3 15-7.3-6M21.5 4.5 9.2 13.5v5.3l3-3.3',
  email: 'M3 6h18v12H3zM3 7l9 6 9-6',
  github:
    'M9 19c-4.3 1.4-4.3-2.5-6-3m12 5v-3.5c0-1 .1-1.4-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.2 4.2 0 0 0-.1-3.2s-1.1-.3-3.5 1.3a12.3 12.3 0 0 0-6.2 0C6.5 2.8 5.4 3.1 5.4 3.1a4.2 4.2 0 0 0-.1 3.2A4.6 4.6 0 0 0 4 9.5c0 4.6 2.7 5.7 5.5 6-.6.6-.6 1.2-.5 2V21',
  network:
    'M12 3v6m0 0H6a2 2 0 0 0-2 2v2m8-4h6a2 2 0 0 1 2 2v2M4 13v2m8-6v8m8-8v2M2 15h4v4H2zm8 0h4v4h-4zm8 0h4v4h-4z',
  server: 'M3 4h18v6H3zm0 10h18v6H3zM7 7h.01M7 17h.01',
  storage:
    'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zm0 0v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
  devops: 'M8 3a5 5 0 0 0 0 10h8a5 5 0 0 1 0 10H8m0-20 3-3M8 3l3 3m5 17-3-3m3 3-3 3',
  os: 'M4 5h16v11H4zM2 19h20M9 16v3m6-3v3',
  code: 'm8 7-5 5 5 5m8-10 5 5-5 5M14 4l-4 16',
};

/** The path of an icon; unknown ids get a neutral one instead of an empty square. */
export function iconPath(id: string, fallback = 'code'): string {
  return ICONS[id] ?? ICONS[fallback] ?? '';
}
