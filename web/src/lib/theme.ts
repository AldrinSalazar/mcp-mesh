// Light and dark themes. public/theme.js applies the saved choice before the
// first paint; this keeps it in sync afterwards.
import { ref } from 'vue'

export type Theme = 'light' | 'dark'

const key = 'mcpmesh.theme'

export const theme = ref<Theme>(document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light')

export function setTheme(t: Theme): void {
  theme.value = t
  if (t === 'dark') document.documentElement.dataset.theme = 'dark'
  else delete document.documentElement.dataset.theme
  try {
    localStorage.setItem(key, t)
  } catch {
    // private mode: the choice lasts for this page only
  }
}

export function toggleTheme(): void {
  setTheme(theme.value === 'dark' ? 'light' : 'dark')
}
