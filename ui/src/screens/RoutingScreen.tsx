import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'

/** Placeholder until this screen's own PR lands (tracking: #163). */
export function RoutingScreen() {
  return (
    <ScreenLayout
      sideLabel="Routing"
      side={null}
      main={<h1 style={{ padding: 24, fontSize: 17 }}>Routing</h1>}
      inspectorLabel="Details"
      inspector={null}
    />
  )
}
