// What an admin does in the dashboard, in a real browser against the mock
// API: sign in, connect a machine, let one join, connect a client, scope a
// group.
import { expect, test, type Page } from '@playwright/test'

// The mock API keeps one session for everyone; each test starts signed out.
test.beforeEach(async ({ request }) => {
  await request.delete('/api/v1/session')
})

async function signIn(page: Page) {
  await page.goto('/')
  await expect(page).toHaveURL(/\/sign-in/)
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill('anything')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
}

test('a wrong password is refused, the right one signs in', async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveURL(/\/sign-in/)
  await page.getByLabel('Username').fill('sam')
  await page.getByLabel('Password').fill('not-the-password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByText('wrong username or password')).toBeVisible()
  await page.getByLabel('Password').fill('password1234')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
})

test('a deep link survives a reload', async ({ page }) => {
  await signIn(page)
  await page.goto('/tools')
  await expect(page.getByRole('heading', { name: 'Tools' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Tools' })).toBeVisible()
})

test('a new machine gets one setup command that names it', async ({ page }) => {
  await signIn(page)
  await page.goto('/keys')
  await page.getByRole('button', { name: 'Create key' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('radio', { name: /^Node key/ }).click()
  await dialog.getByLabel('Machine name').fill('render-box')
  await dialog.getByRole('button', { name: /create/i }).click()
  await expect(page.getByText('Set up RENDER-BOX')).toBeVisible()
  // In the dialog: the Keys page has a pairing command of its own.
  const setup = dialog.locator('code', { hasText: 'mcp-mesh-node init' })
  await expect(setup).toContainText('--name render-box')
  await expect(setup).toContainText('--hub ')
  await expect(setup).toContainText('--key mm_node_')
  await expect(dialog.locator('code', { hasText: 'mcp-mesh-node key set' })).toBeVisible()
  await page.getByRole('button', { name: 'I’ve saved the key' }).click()
  await expect(page.getByText('render-box')).toBeVisible()
  // The secret was shown once: it isn't on the page any more.
  await expect(page.locator('code', { hasText: 'mm_node_' })).toHaveCount(0)
})

test('a machine that asks to join is approved under another name', async ({ page }) => {
  await signIn(page)
  await expect(page.getByText('render-box asks to join')).toBeVisible()
  await page.goto('/approvals')
  const requests = page.getByRole('region', { name: 'Machines asking to join' })
  await expect(requests.getByLabel('Code K7Q-4MX')).toBeVisible()
  await requests.getByRole('listitem').filter({ hasText: 'render-box' }).getByRole('button', { name: 'Review' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Pairing code')).toHaveText('K7Q-4MX')
  await dialog.getByLabel('Name in the mesh').fill('render 01')
  await expect(dialog.getByText('Use a name without spaces.')).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Approve' })).toBeDisabled()
  await dialog.getByLabel('Name in the mesh').fill('render-01')
  await dialog.getByRole('button', { name: 'Approve' }).click()
  await expect(page.getByText('render-01 received its key')).toBeVisible()
  await expect(requests.getByText('render-box')).toHaveCount(0)
  await page.goto('/keys')
  await expect(page.getByRole('row', { name: /render-01/ })).toBeVisible()
})

test('a request for a name in use replaces its key, or is declined', async ({ page }) => {
  await signIn(page)
  await page.goto('/approvals')
  const row = page.getByRole('region', { name: 'Machines asking to join' }).getByRole('listitem').filter({ hasText: 'laptop' })
  await expect(row.getByText('Name in use')).toBeVisible()
  await row.getByRole('button', { name: 'Review' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('A node key for laptop exists')).toBeVisible()
  await expect(dialog.getByRole('button', { name: 'Replace key and approve' })).toBeVisible()
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await row.getByRole('button', { name: 'Decline' }).click()
  await expect(page.getByText('laptop was told no.')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Machines asking to join' })).toHaveCount(0)
})

test('a client key comes with the Claude Code command', async ({ page }) => {
  await signIn(page)
  await page.goto('/keys')
  await page.getByRole('button', { name: 'Create key' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('radio', { name: /^Client key/ }).click()
  await dialog.getByLabel('Client name').fill('e2e-agent')
  await dialog.getByRole('button', { name: /create/i }).click()
  // The command is shown wrapped over several lines.
  const command = page.locator('code', { hasText: 'claude mcp add' })
  await expect(command).toContainText('--transport http mcp-mesh')
  await expect(command).toContainText('Authorization: Bearer mm_client_')
})

// Ticking every tool of an MCP grants those tools, not the MCP
// "now and later"; only ticking the MCP row does that.
test('ticking every tool of an MCP is not a wildcard', async ({ page }) => {
  await signIn(page)
  await page.goto('/groups')
  await page.getByRole('button', { name: 'New group' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill('Git log only')
  await dialog.getByRole('button', { name: 'Expand workstation' }).click()
  await dialog.getByRole('button', { name: 'Expand git' }).click()
  // git on the workstation has a single tool.
  await dialog.getByRole('checkbox', { name: 'git_tool_1' }).check()
  await expect(dialog.getByRole('checkbox', { name: 'git on workstation' })).toBeChecked()
  await expect(dialog.getByText('now and later', { exact: true })).toHaveCount(0)
  await dialog.getByRole('button', { name: /create|save/i }).click()
  const row = page.getByRole('row', { name: /Git log only/ })
  await expect(row).toContainText('1 tool on workstation')

  await page.getByRole('button', { name: 'New group' }).click()
  await dialog.getByLabel('Name').fill('All of git')
  await dialog.getByRole('button', { name: 'Expand workstation' }).click()
  await dialog.getByRole('checkbox', { name: 'git on workstation' }).check()
  await expect(dialog.getByText('now and later', { exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: /create|save/i }).click()
  await expect(page.getByRole('row', { name: /All of git/ })).toContainText('All of git on workstation')
})

test('signing out ends the session', async ({ page }) => {
  await signIn(page)
  await page.getByRole('button', { name: 'admin', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/sign-in/)
  await page.goto('/keys')
  await expect(page).toHaveURL(/\/sign-in/)
})
