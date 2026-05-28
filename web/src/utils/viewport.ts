const supportsDynamicViewport =
  typeof window !== 'undefined' &&
  typeof CSS !== 'undefined' &&
  typeof CSS.supports === 'function' &&
  CSS.supports('height', '100dvh');

export const viewportHeight = (dynamicValue: string, fallbackValue: string): string =>
  supportsDynamicViewport ? dynamicValue : fallbackValue;
