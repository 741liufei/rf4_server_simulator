"""Synthetic fixtures only; no client assets needed by these tests."""
import math
from types import SimpleNamespace as NS
import unittest

import numpy as np

from fish_query_water import Scene, TriangleSurface, build_grid, encode_samples, sample_grid, sample_heights


class WaterGridTests(unittest.TestCase):
    def test_any_of_five_points(self):
        points = np.vstack([np.zeros((1, 5), dtype=bool), np.eye(5, dtype=bool), np.ones((1, 5), dtype=bool)])
        self.assertEqual(encode_samples(points), [0, 1, 2, 4, 8, 16, 31])
        scene = NS(
            terrain_heights=lambda p: (np.zeros(len(p)), 1),
            sample_water=lambda p, g, b: (points.flatten(), []),
        )
        grid, audit = build_grid(scene, {'bounds': [0, 0, 40, 280]})
        self.assertEqual(grid['samples'], [0, 1, 2, 4, 8, 16, 31])
        self.assertEqual((audit['kept'], audit['removed'], audit['all_five_water']), (6, 1, 1))

    def test_clipped_negative_grid_and_zero_upper_edge(self):
        ix, iz, width, height, points = sample_grid([-50, -21, 0, 51])
        self.assertEqual((ix, iz, width, height), (-2, -1, 2, 3))
        np.testing.assert_array_equal(points[0], [-45, -10.5])
        np.testing.assert_array_equal(points[1], [-50, -21])
        for cell, samples in enumerate(points.reshape(-1, 5, 2)):
            for x, z in samples:
                self.assertEqual((math.floor(x/40), math.floor(z/40)), (ix+cell//height, iz+cell%height))
                self.assertTrue(-50 <= x < 0 and -21 <= z < 51)
        for bounds in ([0, 0, 0, 40], [0, 0, math.inf, 40], [0, 0, 4000, 40]):
            with self.assertRaises(ValueError):
                sample_grid(bounds)

    def test_height_axes_interpolation_and_origin(self):
        points = np.array([[-10, -20], [0, -20], [-10, 0], [-5, -10], [1, 0]])
        heights = np.array([[0, .2], [.6, 1.]])  # z rows, x columns
        got = sample_heights(heights, [-10, -7, -20], {'x': 10, 'y': 100, 'z': 20}, points)
        np.testing.assert_allclose(got, [-7, 13, 53, 38, -np.inf])

    def test_overlapping_terrain_uses_highest_surface(self):
        scene = object.__new__(Scene)
        scene.objects = []
        def add_terrain(origin, raw, enabled=True):
            data = {'m_Heightmap': {'m_Resolution': 2, 'm_Heights': [raw]*4, 'm_Holes': [],
                    'm_Scale': {'x': 40, 'y': 10, 'z': 40}, 'm_MinMaxPatchHeights': [raw/32766]*2}}
            component = NS(m_Enabled=enabled, m_GameObject=origin, m_TerrainData=NS(read_typetree=lambda: data))
            scene.objects.append(NS(type=NS(name='Terrain'), read=lambda: component))
        def game_object(origin):
            matrix = np.eye(4)
            matrix[:3, 3] = origin
            return None, None, matrix, True
        scene.game_object = game_object
        add_terrain([0, -10, 0], 0)
        add_terrain([10, 0, 10], 16383)
        add_terrain([0, 100, 0], 0, enabled=False)
        values, count = scene.terrain_heights(np.array([[5, 5], [20, 20], [10, 10]]))
        np.testing.assert_array_equal(values, [-10, 5, 5])
        self.assertEqual(count, 2)
        with self.assertRaisesRegex(ValueError, 'lack terrain coverage'):
            scene.terrain_heights(np.array([[100, 100]]))

    def test_triangle_footprint_and_negative_boundary(self):
        surface = TriangleSurface([[-40, 0, -40], [0, 0, -40], [-40, 0, 0]],
                                  [[0, 1, 2], [0, 0, 0]], [-40, -40, 0, 0])
        points = np.array([[-40, -40], [-30, -30], [-20, -20], [-1, -1], [1, -20]])
        np.testing.assert_array_equal(surface.contains(points), [True, True, True, False, False])
        _, _, _, _, corners = sample_grid([-40, -40, 0, 0])
        np.testing.assert_array_equal(surface.contains(corners), [True, True, True, True, False])

    def test_trigger_requires_submerged_ground(self):
        scene = object.__new__(Scene)
        matrix = np.eye(4)
        matrix[1, 3] = 7  # Not all maps need to have baseline water at zero.
        go = NS(object_reader=NS(path_id=10))
        box = NS(object_reader=NS(type=NS(name='BoxCollider')), m_Enabled=True, m_IsTrigger=True,
                 m_Center=NS(x=0, y=-5, z=0), m_Size=NS(x=40, y=10, z=40))
        transform = NS(object_reader=NS(type=NS(name='Transform')), m_Father=NS(m_PathID=0))
        component = NS(m_Enabled=True, m_GameObject=None)
        scene.objects = [NS(type=NS(name='MonoBehaviour'), read=lambda **kwargs: component)]
        scene.controlled_objects = lambda: {10}
        scene.script_name = lambda c: 'Water2'
        scene.game_object = lambda ptr: (go, [box, transform], matrix, False)
        points = np.array([[0, 0], [0, 0], [0, 0], [30, 0]])
        wet, _ = scene.sample_water(points, np.array([6, 7, 8, 0]), [-40, -40, 40, 40])
        np.testing.assert_array_equal(wet, [True, False, False, False])
        scene.controlled_objects = lambda: set()
        with self.assertRaisesRegex(ValueError, 'No supported active or controlled water body'):
            scene.sample_water(points, np.zeros(4), [-40, -40, 40, 40])


if __name__ == '__main__':
    unittest.main()
