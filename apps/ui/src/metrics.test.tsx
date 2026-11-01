import { describe, test, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import MetricsPage from './pages/MetricsPage'

afterEach(cleanup)

// Against the real server binary: the page asks /api/metrics and renders
// whatever window it gets, including an empty one.
describe('the Metrics page', () => {
  test('renders the aggregate and switches windows', async () => {
    render(<MetricsPage />)
    await screen.findByText('Runs over time')
    expect(screen.getByText('Step failure rate')).toBeTruthy()
    expect(screen.getByText('Models')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '30d' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '30d' }).getAttribute('aria-pressed')).toBe('true'))
    await screen.findByText('Runs over time')
  })
})
