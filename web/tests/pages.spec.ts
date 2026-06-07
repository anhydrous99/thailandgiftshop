import { expect, test, type Page } from '@playwright/test';

const excludedControlNames = ['Account', 'Search', 'Filter', 'Sort'];

const implementedPagesWithoutCartState = [
  '/',
  '/products',
  '/products/mango-sticky-rice-kit',
  '/categories',
  '/categories/market-finds',
  '/story',
  '/cart',
];

function expectCanonicalPath(pageURL: string, path: string) {
  expect(new URL(pageURL).pathname).toBe(path);
}

async function expectExcludedControlsAbsent(page: Page) {
  for (const name of excludedControlNames) {
    await expect(page.getByRole('link', { name, exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0);
  }
}

async function expectNoPaymentOrPIIControls(page: Page) {
  await expect(page.getByRole('button', { name: /place order|pay now/i })).toHaveCount(0);
  await expect(page.getByRole('link', { name: /place order|pay now/i })).toHaveCount(0);
  await expect(page.getByLabel(/card number/i)).toHaveCount(0);
  await expect(page.locator('input[name="email"], input[name="address"], input[name="card"], input[autocomplete^="cc-"]')).toHaveCount(0);
}

test('catalog slug navigation clicks category and product cards to canonical URLs', async ({ page }) => {
  await page.goto('/');

  await page.getByRole('link', { name: /Bangkok Market Finds/ }).first().click();
  await expect(page).toHaveURL(/\/categories\/market-finds$/);
  expectCanonicalPath(page.url(), '/categories/market-finds');
  await expect(page.getByRole('heading', { name: 'Bangkok Market Finds', level: 1 })).toBeVisible();

  await page.goto('/products');
  await page.getByRole('link', { name: /Mango Sticky Rice Treats/ }).click();
  await expect(page).toHaveURL(/\/products\/mango-sticky-rice-kit$/);
  expectCanonicalPath(page.url(), '/products/mango-sticky-rice-kit');
  await expect(page.getByRole('heading', { name: 'Mango Sticky Rice Treats', level: 1 })).toBeVisible();
});

test('legacy and trailing-slash redirects resolve to canonical URLs', async ({ page }) => {
  const redirects = [
    { from: '/shop', to: '/products' },
    { from: '/about', to: '/story' },
    { from: '/products/', to: '/products' },
    { from: '/categories/', to: '/categories' },
    { from: '/story/', to: '/story' },
    { from: '/cart/', to: '/cart' },
    { from: '/checkout/', to: '/cart' },
  ];

  for (const redirect of redirects) {
    await page.goto(redirect.from);
    expectCanonicalPath(page.url(), redirect.to);
  }
});

test('implemented pages keep account search filter and sort controls absent', async ({ page }) => {
  for (const path of implementedPagesWithoutCartState) {
    await page.goto(path);
    await expectExcludedControlsAbsent(page);
  }
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  test('catalog pages render without JavaScript', async ({ page }) => {
    await page.goto('/products');
    await expect(page.getByRole('heading', { name: 'Products', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: /Mango Sticky Rice Treats/ })).toBeVisible();

    await page.goto('/products/mango-sticky-rice-kit');
    await expect(page.getByRole('heading', { name: 'Mango Sticky Rice Treats', level: 1 })).toBeVisible();
    await expect(page.getByText('Coconut cream, sweet rice, mango candy')).toBeVisible();

    await page.goto('/categories');
    await expect(page.getByRole('heading', { name: 'Categories', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: /Bangkok Market Finds/ })).toBeVisible();

    await page.goto('/categories/market-finds');
    await expect(page.getByRole('heading', { name: 'Bangkok Market Finds', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: /Mango Sticky Rice Treats/ })).toBeVisible();

    await page.goto('/story');
    await expect(page.getByRole('heading', { name: 'Our Story', level: 1 })).toBeVisible();
    await expect(page.getByText('slug-based links that work without JavaScript')).toBeVisible();
  });

  test('no-js cart checkout flow adds updates persists removes and reviews', async ({ page }) => {
    await page.goto('/checkout');
    await expect(page).toHaveURL(/\/cart$/);
    await expect(page.getByRole('heading', { name: 'Cart', level: 1 })).toBeVisible();
    await expect(page.getByText('Your cart is empty.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Review checkout' })).toHaveCount(0);
    await expectExcludedControlsAbsent(page);
    await expectNoPaymentOrPIIControls(page);

    await page.goto('/products/mango-sticky-rice-kit');
    await expect(page.getByRole('heading', { name: 'Mango Sticky Rice Treats', level: 1 })).toBeVisible();
    await page.getByRole('button', { name: 'Add to cart' }).click();

    await expect(page).toHaveURL(/\/cart$/);
    await expect(page.getByRole('heading', { name: 'Cart', level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Cart (1)', exact: true }).first()).toBeVisible();

    const cartLine = page.getByTestId('cart-line-item').filter({ hasText: 'Mango Sticky Rice Treats' });
    await expect(cartLine).toBeVisible();
    await expect(cartLine.getByText('$28.99')).toHaveCount(2);
    await cartLine.getByLabel('Quantity').fill('2');
    await cartLine.getByRole('button', { name: 'Update' }).click();

    await expect(page).toHaveURL(/\/cart$/);
    const updatedCartLine = page.getByTestId('cart-line-item').filter({ hasText: 'Mango Sticky Rice Treats' });
    await expect(updatedCartLine.getByLabel('Quantity')).toHaveValue('2');
    await expect(updatedCartLine.getByText('$57.98')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Cart (2)', exact: true }).first()).toBeVisible();

    await page.reload();
    const persistedCartLine = page.getByTestId('cart-line-item').filter({ hasText: 'Mango Sticky Rice Treats' });
    await expect(page).toHaveURL(/\/cart$/);
    await expect(persistedCartLine.getByLabel('Quantity')).toHaveValue('2');
    await expect(persistedCartLine.getByText('$57.98')).toBeVisible();
    await expectExcludedControlsAbsent(page);
    await expectNoPaymentOrPIIControls(page);

    await page.getByRole('link', { name: 'Review checkout' }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await expect(page.getByRole('heading', { name: 'Checkout Review', level: 1 })).toBeVisible();
    const checkoutLine = page.getByTestId('checkout-line-item').filter({ hasText: 'Mango Sticky Rice Treats' });
    await expect(checkoutLine).toBeVisible();
    await expect(checkoutLine.getByText('Quantity 2 at')).toBeVisible();
    await expect(checkoutLine.getByText('Line total $57.98')).toBeVisible();
    await expect(page.locator('form')).toHaveCount(0);
    await expectExcludedControlsAbsent(page);
    await expectNoPaymentOrPIIControls(page);

    await page.getByRole('link', { name: 'Back to cart' }).first().click();
    await expect(page).toHaveURL(/\/cart$/);
    await page.getByRole('button', { name: 'Remove' }).click();
    await expect(page).toHaveURL(/\/cart$/);
    await expect(page.getByText('Your cart is empty.')).toBeVisible();
    await expect(page.getByTestId('cart-line-item')).toHaveCount(0);

    await page.goto('/products/mango-sticky-rice-kit');
    await page.getByRole('button', { name: 'Add to cart' }).click();
    await expect(page).toHaveURL(/\/cart$/);
    await page.getByRole('link', { name: 'Review checkout' }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await expect(page.getByRole('heading', { name: 'Checkout Review', level: 1 })).toBeVisible();
    await expect(page.getByTestId('checkout-line-item').filter({ hasText: 'Mango Sticky Rice Treats' })).toBeVisible();
    await expect(page.locator('form')).toHaveCount(0);
    await expectExcludedControlsAbsent(page);
    await expectNoPaymentOrPIIControls(page);
  });
});
