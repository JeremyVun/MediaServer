import { expect, test } from '@playwright/test'

test.use({ viewport: { width: 390, height: 844 } })

test('the selected season is fully visible on load at 22 seasons on a phone', async ({ page }) => {
  await page.goto('/demo')
  const row = page.locator('[data-demo="season-chips-22"] [role="tablist"]').first()
  const selected = row.getByRole('tab', { selected: true })
  await expect(selected).toHaveAccessibleName('Season 21')
  expect(await row.evaluate((el) => el.scrollWidth > el.clientWidth * 2)).toBe(true)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)

  const inView = async () => {
    const [r, c] = await Promise.all([row.boundingBox(), selected.boundingBox()])
    return !!r && !!c && c.x >= r.x && c.x + c.width <= r.x + r.width
  }
  await expect.poll(inView).toBe(true)

  const tabs = row.getByRole('tab')
  await expect(tabs.last()).toHaveAccessibleName('Specials, none in your library')
  expect((await tabs.first().boundingBox())?.height).toBeGreaterThanOrEqual(44)
  await expect(tabs.nth(4).locator('span')).toHaveCSS('border-style', 'dashed')

  await row.getByRole('tab', { name: 'Season 5, none in your library' }).click()
  await expect(row.getByRole('tab', { selected: true })).toHaveAccessibleName('Season 5, none in your library')
  await expect.poll(inView).toBe(true)
})
