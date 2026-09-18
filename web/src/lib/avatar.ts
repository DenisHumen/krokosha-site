// Avatar images (AVIF + WebP, several widths) prepared at build time — served locally, never hotlinked.
// Source, first that exists:
//   1. content/avatar.*             — a picture Denis put there by hand
//   2. content/generated/avatar.*   — downloaded from GitHub by the sync
//   3. src/assets/avatar-fallback.png — neutral placeholder (CI, fresh clone)

import type { ImageMetadata } from 'astro';
import { getImage } from 'astro:assets';
import fallback from '../assets/avatar-fallback.png';

const manual = import.meta.glob<ImageMetadata>('../../../content/avatar.{png,jpg,jpeg,webp,avif}', {
  eager: true,
  import: 'default',
});
const synced = import.meta.glob<ImageMetadata>(
  '../../../content/generated/avatar.{png,jpg,jpeg,webp,avif}',
  { eager: true, import: 'default' },
);

/** Widths for 1×–3× screens; the image service never upscales, so they are capped by the source. */
const WIDTHS = [128, 256, 512];

export interface AvatarImages {
  avif: { src: string; srcset: string };
  webp: { src: string; srcset: string };
  /** Largest WebP — for Open Graph and JSON-LD. */
  largest: string;
}

function source(): ImageMetadata {
  return Object.values(manual)[0] ?? Object.values(synced)[0] ?? fallback;
}

let cached: Promise<AvatarImages> | undefined;

export function getAvatar(): Promise<AvatarImages> {
  cached ??= (async () => {
    const src = source();
    const widths = [...new Set(WIDTHS.map((width) => Math.min(width, src.width)))];
    const largestWidth = Math.max(...widths);
    const variant = async (format: 'avif' | 'webp') => {
      const image = await getImage({ src, format, widths, width: 256, height: 256 });
      return { src: image.src, srcset: image.srcSet.attribute };
    };
    const largest = await getImage({
      src,
      format: 'webp',
      width: largestWidth,
      height: largestWidth,
    });
    return { avif: await variant('avif'), webp: await variant('webp'), largest: largest.src };
  })();
  return cached;
}
