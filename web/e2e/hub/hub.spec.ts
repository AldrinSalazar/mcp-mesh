// The dashboard as the hub serves it (embedded build, real API), on a hub
// the VM end-to-end tests set up: one node, node-a, sharing the fake MCP.
import { expect, test } from '@playwright/test'

const password = process.env.MCPMESH_E2E_ADMIN_PASSWORD ?? ''

test('the admin signs in and sees the machine and its tools', async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveURL(/\/sign-in/)
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()

  await page.goto('/nodes')
  await expect(page.getByText('node-a').first()).toBeVisible()
  await page.goto('/tools')
  await expect(page.getByText('node-a__fake__render').first()).toBeVisible()

  // A new machine's setup command points at this hub's real node URL.
  await page.goto('/keys')
  await page.getByRole('button', { name: 'Create key' }).first().click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('radio', { name: /^Node key/ }).click()
  await dialog.getByLabel('Machine name').fill('browser-made')
  await dialog.getByRole('button', { name: /create/i }).click()
  const setup = page.locator('code', { hasText: 'mcp-mesh-node init' })
  await expect(setup).toContainText(`--hub ${process.env.MCPMESH_E2E_NODE_URL}`)
  await expect(setup).toContainText('--name browser-made')
  await page.getByRole('button', { name: 'I’ve saved the key' }).click()
})
