module.exports = {
  content: ['./src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // Severity colors are defined once here and mirrored by the Go risk
        // model, so a severity rendered in the terminal and one rendered in
        // the dashboard are the same hue.
        sev: {
          critical: '#dc2626',
          high: '#ea580c',
          medium: '#ca8a04',
          low: '#2563eb',
          info: '#64748b',
        },
      },
    },
  },
  plugins: [],
};