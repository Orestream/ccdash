// Helpers for clipboard-pasted images in the composer.
import type { ImageInput } from '../types';

// A pasted-but-not-yet-sent image. dataUrl is a `data:` URL used for preview and
// (after stripping the prefix) as the base64 payload sent to the backend.
export interface PendingImage {
  name: string;
  mediaType: string;
  dataUrl: string;
}

const EXT_BY_TYPE: Record<string, string> = {
  'image/png': 'png',
  'image/jpeg': 'jpg',
  'image/gif': 'gif',
  'image/webp': 'webp',
};

export function extForMediaType(mediaType: string): string {
  return EXT_BY_TYPE[mediaType] ?? mediaType.split('/')[1] ?? 'png';
}

// renumber assigns sequential names (image-1.png, image-2.jpg, …) by position,
// so the names always match what the user sees and can reference in text — even
// after one is removed.
export function renumber(images: Omit<PendingImage, 'name'>[]): PendingImage[] {
  return images.map((img, i) => ({
    ...img,
    name: `image-${i + 1}.${extForMediaType(img.mediaType)}`,
  }));
}

// toImageInput strips the `data:<type>;base64,` prefix, leaving the raw base64
// the API expects.
export function toImageInput(images: PendingImage[]): ImageInput[] {
  return images.map((img) => ({
    name: img.name,
    mediaType: img.mediaType,
    data: img.dataUrl.slice(img.dataUrl.indexOf(',') + 1),
  }));
}

// imagesFromClipboard extracts image files from a paste event's clipboard items.
export function imagesFromClipboard(items: DataTransferItemList | null | undefined): File[] {
  if (!items) return [];
  const files: File[] = [];
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    if (it.kind === 'file' && it.type.startsWith('image/')) {
      const f = it.getAsFile();
      if (f) files.push(f);
    }
  }
  return files;
}
