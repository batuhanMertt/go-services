import { Navigate, Route, Routes } from 'react-router-dom'

import Layout from './components/Layout'
import { LoadingBlock } from './components/ui'
import { useAuth } from './lib/auth'

import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import Tools from './pages/Tools'
import EventTypes from './pages/EventTypes'
import Subscriptions from './pages/Subscriptions'
import RoutingRules from './pages/RoutingRules'
import Events from './pages/Events'
import Deliveries from './pages/Deliveries'
import Dlq from './pages/Dlq'
import Audit from './pages/Audit'
import Settings from './pages/Settings'

export default function App() {
  const { user, ready } = useAuth()

  // Oturum durumu belirlenene kadar hiçbir şey render edilmez: aksi halde
  // giriş yapmış kullanıcı bir an login ekranını görürdü.
  if (!ready) {
    return (
      <div className="grid min-h-screen place-items-center bg-surface-base">
        <LoadingBlock />
      </div>
    )
  }

  if (!user) {
    return (
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    )
  }

  return (
    <Routes>
      <Route path="/login" element={<Navigate to="/" replace />} />
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="tools" element={<Tools />} />
        <Route path="event-types" element={<EventTypes />} />
        <Route path="subscriptions" element={<Subscriptions />} />
        <Route path="routing-rules" element={<RoutingRules />} />
        <Route path="events" element={<Events />} />
        <Route path="deliveries" element={<Deliveries />} />
        <Route path="dlq" element={<Dlq />} />
        <Route path="audit" element={<Audit />} />
        <Route path="settings" element={<Settings />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}
