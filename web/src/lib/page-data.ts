// Data for Astro pages: content + prepared avatar, one object per language.

import type { Locale } from '../i18n/locales.ts';
import { getAvatar, type AvatarImages } from './avatar.ts';
import { getContent } from './content.ts';
import { buildPageData, type PageData } from './data.ts';

/** One moment for the whole build, so every page shows the same experience and year. */
const buildTime = new Date();

export interface SitePageData extends PageData {
  avatarImages: AvatarImages;
  buildTime: Date;
}

export async function getPageData(lang: Locale): Promise<SitePageData> {
  const avatarImages = await getAvatar();
  const data = buildPageData(getContent(), lang, {
    now: buildTime,
    mode: 'site',
    avatar: avatarImages.avif,
  });
  return { ...data, avatarImages, buildTime };
}
