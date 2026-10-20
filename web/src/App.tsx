import { Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from './components/AppLayout'
import { RequireAuth } from './components/RequireAuth'
import { ToastProvider } from './components/Toast'
import { AuthProvider } from './lib/auth'
import AlertsPage from './pages/AlertsPage'
import AppDetailPage from './pages/AppDetailPage'
import AppsPage from './pages/AppsPage'
import BackupsPage from './pages/BackupsPage'
import ChecksPage from './pages/ChecksPage'
import CredentialsPage from './pages/CredentialsPage'
import DashboardPage from './pages/DashboardPage'
import LoginPage from './pages/LoginPage'
import ReleaseDetailPage from './pages/ReleaseDetailPage'
import ServerDetailPage from './pages/ServerDetailPage'

export default function App() {
  return (
    <ToastProvider>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route
            element={
              <RequireAuth>
                <AppLayout />
              </RequireAuth>
            }
          >
            <Route index element={<DashboardPage />} />
            <Route path="/checks" element={<ChecksPage />} />
            <Route path="/alerts" element={<AlertsPage />} />
            <Route path="/apps" element={<AppsPage />} />
            <Route path="/backups" element={<BackupsPage />} />
            <Route path="/apps/:id" element={<AppDetailPage />} />
            <Route path="/releases/:id" element={<ReleaseDetailPage />} />
            <Route path="/credentials" element={<CredentialsPage />} />
            <Route path="/servers/:id" element={<ServerDetailPage />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </AuthProvider>
    </ToastProvider>
  )
}
