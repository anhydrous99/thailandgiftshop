import { expect, test, type Page } from '@playwright/test';

const excludedControlNames = ['Search', 'Filter', 'Sort'];

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

async function expectAccountEntryPoint(page: Page) {
  await expect(page.getByRole('link', { name: 'Account', exact: true }).first()).toHaveAttribute('href', '/account');
}

async function expectCheckoutEntryPoint(page: Page) {
  await expect(page.getByRole('link', { name: 'Check out', exact: true })).toHaveAttribute('href', '/checkout');
}

test('variant picker reflects the selected size stock into the quantity limit and a live hint', async ({ page }) => {
  await page.goto('/products/handwoven-indigo-scarf');

  const variant = page.getByTestId('variant-select');
  const quantity = page.getByLabel('Quantity');
  const hint = page.locator('[data-variant-stock-hint]');

  // No size chosen yet — the hint stays hidden.
  await expect(hint).toBeHidden();

  // Pre-fill an over-limit quantity, then pick the well-stocked M size.
  // enhance.js clamps the quantity, mirrors the size's stock into the max, and
  // shows a live hint. Stock is read from the DOM so the test stays correct
  // regardless of how much of the shared demo catalog other tests have used.
  await quantity.fill('50');
  await variant.selectOption({ label: 'M' });
  const stock = await variant.locator('option:checked').getAttribute('data-stock');
  await expect(quantity).toHaveAttribute('max', stock!);
  await expect(quantity).toHaveValue(stock!);
  await expect(hint).toHaveText(`${stock} left in this size`);
});

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
    // An anonymous empty-cart visit to /checkout still lands on /cart.
    { from: '/checkout/', to: '/cart' },
    { from: '/account/sign-in/', to: '/account/sign-in' },
    { from: '/account/sign-up/', to: '/account/sign-up' },
    // Protected pages bounce anonymous visitors to sign-in after the 308.
    { from: '/account/', to: '/account/sign-in' },
    { from: '/orders/', to: '/account/sign-in' },
  ];

  for (const redirect of redirects) {
    await page.goto(redirect.from);
    expectCanonicalPath(page.url(), redirect.to);
  }
});

test('account checkout and orders trailing-slash paths emit permanent redirects to canonical paths', async ({ page }) => {
  const redirects = [
    { from: '/account/', to: '/account' },
    { from: '/account/sign-in/', to: '/account/sign-in' },
    { from: '/account/sign-up/', to: '/account/sign-up' },
    { from: '/account/addresses/', to: '/account/addresses' },
    { from: '/account/payment-methods/', to: '/account/payment-methods' },
    { from: '/orders/', to: '/orders' },
    { from: '/checkout/', to: '/checkout' },
    { from: '/checkout/confirm/', to: '/checkout/confirm' },
    { from: '/checkout/fake-pay/', to: '/checkout/fake-pay' },
  ];

  for (const redirect of redirects) {
    const response = await page.request.get(redirect.from, { maxRedirects: 0 });
    expect(response.status(), `${redirect.from} should permanently redirect`).toBe(308);
    expect(response.headers()['location'], `${redirect.from} location`).toBe(redirect.to);
  }
});

test('implemented pages expose the account entry point and keep search filter and sort controls absent', async ({ page }) => {
  for (const path of implementedPagesWithoutCartState) {
    await page.goto(path);
    await expectExcludedControlsAbsent(page);
    await expectAccountEntryPoint(page);
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

  test('no-js cart flow adds updates persists removes and hands off to guest checkout', async ({ page }) => {
    await page.goto('/checkout');
    await expect(page).toHaveURL(/\/cart$/);
    await expect(page.getByRole('heading', { name: 'Cart', level: 1 })).toBeVisible();
    await expect(page.getByText('Your cart is empty.')).toBeVisible();
    await expect(page.getByRole('link', { name: 'Check out', exact: true })).toHaveCount(0);
    await expectExcludedControlsAbsent(page);
    await expectAccountEntryPoint(page);

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
    await expectCheckoutEntryPoint(page);

    // Checkout renders the guest layout for anonymous shoppers; the sign-in
    // link still reaches the account flow with a return_to back to checkout.
    await page.getByRole('link', { name: 'Check out', exact: true }).click();
    await expect(page).toHaveURL(/\/checkout$/);
    await expect(page.getByTestId('guest-checkout-form')).toBeVisible();
    await expect(page.getByTestId('guest-email-input')).toBeVisible();
    await page.getByTestId('checkout-sign-in-link').click();
    await expect(page).toHaveURL(/\/account\/sign-in\?return_to=%2Fcheckout$/);
    await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible();
    await expect(page.getByTestId('signin-form')).toBeVisible();
    await expect(page.locator('input[name="return_to"]')).toHaveValue('/checkout');

    // The anonymous cart survives the bounce and can still be emptied.
    await page.goto('/cart');
    await expect(page.getByTestId('cart-line-item').filter({ hasText: 'Mango Sticky Rice Treats' })).toBeVisible();
    await page.getByRole('button', { name: 'Remove' }).click();
    await expect(page).toHaveURL(/\/cart$/);
    await expect(page.getByText('Your cart is empty.')).toBeVisible();
    await expect(page.getByTestId('cart-line-item')).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Check out', exact: true })).toHaveCount(0);
  });
});
