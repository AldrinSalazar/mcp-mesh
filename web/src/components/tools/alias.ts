// Same rule as the hub's catalog.ValidAlias, so the dialog can say what is
// wrong before the request: 1 to 64 characters from [A-Za-z0-9_-].
export const MAX_ALIAS = 64
export const MAX_DESCRIPTION = 2000

// aliasError returns why alias can't be used, or '' (empty is fine: it means
// "use the generated name").
export function aliasError(alias: string): string {
  if (alias === '') return ''
  if (alias.length > MAX_ALIAS) return `Use at most ${MAX_ALIAS} characters.`
  if (!/^[A-Za-z0-9_-]+$/.test(alias)) return 'Use only letters, digits, _ and -.'
  return ''
}

// descriptionError mirrors the hub: at most 2000 characters, no control
// characters except newline and tab.
export function descriptionError(text: string): string {
  if ([...text].length > MAX_DESCRIPTION) return `Use at most ${MAX_DESCRIPTION} characters.`
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/.test(text)) return 'Remove control characters.'
  return ''
}
