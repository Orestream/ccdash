import { describe, expect, it } from 'vitest';
import { extForMediaType, renumber, toImageInput } from './pastedImages';

describe('pastedImages helpers', () => {
  it('maps media types to extensions', () => {
    expect(extForMediaType('image/png')).toBe('png');
    expect(extForMediaType('image/jpeg')).toBe('jpg');
    expect(extForMediaType('image/webp')).toBe('webp');
    expect(extForMediaType('image/svg+xml')).toBe('svg+xml');
  });

  it('renumbers sequentially by position', () => {
    const out = renumber([
      { mediaType: 'image/png', dataUrl: 'data:image/png;base64,AAA' },
      { mediaType: 'image/jpeg', dataUrl: 'data:image/jpeg;base64,BBB' },
    ]);
    expect(out.map((i) => i.name)).toEqual(['image-1.png', 'image-2.jpg']);
  });

  it('strips the data URL prefix for the payload', () => {
    const out = toImageInput([
      { name: 'image-1.png', mediaType: 'image/png', dataUrl: 'data:image/png;base64,SGk=' },
    ]);
    expect(out).toEqual([{ name: 'image-1.png', mediaType: 'image/png', data: 'SGk=' }]);
  });
});
