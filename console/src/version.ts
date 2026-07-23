const normalizedVersion = __JCLOUD_VERSION__.trim();

export const CONSOLE_VERSION = normalizedVersion.startsWith('v')
  ? normalizedVersion
  : `v${normalizedVersion}`;
